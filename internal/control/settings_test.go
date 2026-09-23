package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

type memorySettingsRepository struct {
	mu       sync.Mutex
	snapshot SettingsSnapshot
	saves    int
	inflight int
	maxIn    int
	saveErr  error
}

func newMemorySettingsRepository(values config.Settings) *memorySettingsRepository {
	return &memorySettingsRepository{snapshot: SettingsSnapshot{Revision: 1, Values: values, UpdatedAt: time.Now().UTC()}}
}

func (repository *memorySettingsRepository) Load(context.Context) (SettingsSnapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot, nil
}

func (repository *memorySettingsRepository) Save(_ context.Context, expected uint64, values config.Settings) (SettingsSnapshot, error) {
	repository.mu.Lock()
	if repository.snapshot.Revision != expected {
		repository.mu.Unlock()
		return SettingsSnapshot{}, agent.NewError(agent.ErrorConflict, "save", errors.New("revision conflict"))
	}
	repository.inflight++
	if repository.inflight > repository.maxIn {
		repository.maxIn = repository.inflight
	}
	repository.saves++
	err := repository.saveErr
	repository.inflight--
	if err != nil {
		repository.mu.Unlock()
		return SettingsSnapshot{}, err
	}
	repository.snapshot.Revision++
	repository.snapshot.Values = values
	repository.snapshot.UpdatedAt = time.Now().UTC()
	snapshot := repository.snapshot
	repository.mu.Unlock()
	return snapshot, nil
}

func TestGetSettingsReturnsPublicSecretSafeView(t *testing.T) {
	settings := config.DefaultSettings()
	settings.LLMAPIKey = "primary-secret"
	settings.FallbackAPIKey = "fallback-secret"
	settings.LangSmithAPIKey = "trace-secret"
	repository := newMemorySettingsRepository(settings)
	controller, err := NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	view, err := controller.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Values.LLMAPIKey != "" || view.Values.FallbackAPIKey != "" || view.Values.LangSmithAPIKey != "" {
		t.Fatal("public view exposed a secret")
	}
	if !view.Values.LLMAPIKeyConfigured || !view.Values.FallbackAPIKeyConfigured || !view.Values.LangSmithAPIKeyConfigured {
		t.Fatalf("secret status = %#v", view.Values)
	}
}

func TestValidateSettingsDraftDoesNotWriteOrLeakSecret(t *testing.T) {
	settings := config.DefaultSettings()
	settings.LogLevel = "trace"
	settings.LLMAPIKey = "stored-secret"
	repository := newMemorySettingsRepository(settings)
	controller, err := NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.ValidateSettings(context.Background(), SettingsPatch{
		Draft:   settings,
		Secrets: SecretPatch{LLMAPIKey: SecretUpdate{Action: SecretKeep}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Readiness) == 0 {
		t.Fatalf("validation result = %#v", result)
	}
	if repository.saves != 0 || strings.Contains(formatIssues(result.Readiness), "stored-secret") {
		t.Fatal("validation wrote state or exposed the secret")
	}
}

func TestSaveSettingsSecretKeepReplaceAndClear(t *testing.T) {
	settings := config.DefaultSettings()
	settings.LLMAPIKey = "old-primary"
	settings.FallbackEndpoint = "https://fallback.example.invalid/v1"
	settings.FallbackAPIKey = "old-fallback"
	settings.LangSmithAPIKey = "old-trace"
	repository := newMemorySettingsRepository(settings)
	controller, err := NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	settings.AssistantName = "keep"
	saved, err := controller.SaveSettings(ctx, SaveSettingsRequest{
		ExpectedRevision: 1,
		Patch: SettingsPatch{Draft: settings, Secrets: SecretPatch{
			LLMAPIKey:       SecretUpdate{Action: SecretKeep},
			FallbackAPIKey:  SecretUpdate{Action: SecretReplace, Value: "new-fallback"},
			LangSmithAPIKey: SecretUpdate{Action: SecretClear},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.View.Values.LLMAPIKey != "" || !saved.View.Values.LLMAPIKeyConfigured || saved.View.Values.LangSmithAPIKeyConfigured {
		t.Fatalf("public save result = %#v", saved.View.Values)
	}
	if repository.snapshot.Values.LLMAPIKey != "old-primary" || repository.snapshot.Values.FallbackAPIKey != "new-fallback" || repository.snapshot.Values.LangSmithAPIKey != "" {
		t.Fatalf("merged secrets = %#v", repository.snapshot.Values)
	}
}

func TestSaveSettingsUsesExpectedRevisionAndSerializesMutation(t *testing.T) {
	repository := newMemorySettingsRepository(config.DefaultSettings())
	controller, err := NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	patch := SettingsPatch{Draft: config.DefaultSettings()}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, saveErr := controller.Save(ctx, 1, patch)
			results <- saveErr
		}()
	}
	wait.Wait()
	close(results)
	var success, conflicts int
	for saveErr := range results {
		switch {
		case saveErr == nil:
			success++
		case agent.IsCode(saveErr, agent.ErrorConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent save error: %v", saveErr)
		}
	}
	if success != 1 || conflicts != 1 || repository.maxIn != 1 {
		t.Fatalf("success=%d conflicts=%d max concurrent writes=%d", success, conflicts, repository.maxIn)
	}
}

func TestSaveSettingsReturnsSafeRepositoryError(t *testing.T) {
	repository := newMemorySettingsRepository(config.DefaultSettings())
	repository.saveErr = errors.New("sqlite failure: secret-value-must-not-escape")
	controller, err := NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Save(context.Background(), 1, SettingsPatch{Draft: config.DefaultSettings()})
	if err == nil || !agent.IsCode(err, agent.ErrorStorageFailure) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("save error = %v", err)
	}
}

func formatIssues(issues []config.ReadinessIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+":"+issue.Code+":"+issue.Message)
	}
	return strings.Join(parts, ";")
}
