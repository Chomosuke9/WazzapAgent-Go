package control

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

// Load returns a secret-safe settings view. It is an alias for GetSettings for
// callers that model the operation after the repository method.
func (controller *Controller) Load(ctx context.Context) (SettingsView, error) {
	return controller.GetSettings(ctx)
}

func (controller *Controller) GetSettings(ctx context.Context) (SettingsView, error) {
	if controller == nil || controller.repository == nil {
		return SettingsView{}, controllerError("get settings", errors.New("settings controller is unavailable"))
	}
	snapshot, err := controller.repository.Load(ctx)
	if err != nil {
		return SettingsView{}, safeRepositoryError("load settings", err)
	}
	return viewFromSnapshot(snapshot), nil
}

// ValidateSettings validates a draft without writing it. Secret actions are
// merged with the currently stored secret values so keep/clear/replace are
// checked against the same complete typed value SaveSettings would use.
func (controller *Controller) ValidateSettings(ctx context.Context, patch SettingsPatch) (ValidationResult, error) {
	if controller == nil || controller.repository == nil {
		return ValidationResult{}, controllerError("validate settings", errors.New("settings controller is unavailable"))
	}
	snapshot, err := controller.repository.Load(ctx)
	if err != nil {
		return ValidationResult{}, safeRepositoryError("load settings for validation", err)
	}
	merged, err := mergePatch(snapshot.Values, patch)
	if err != nil {
		return ValidationResult{}, err
	}
	return validationResult(merged), nil
}

// Validate is a compact name for ValidateSettings.
func (controller *Controller) Validate(ctx context.Context, patch SettingsPatch) (ValidationResult, error) {
	return controller.ValidateSettings(ctx, patch)
}

func (controller *Controller) Save(ctx context.Context, expectedRevision uint64, patch SettingsPatch) (SaveSettingsResult, error) {
	if controller == nil || controller.repository == nil {
		return SaveSettingsResult{}, controllerError("save settings", errors.New("settings controller is unavailable"))
	}
	controller.mutate.Lock()
	defer controller.mutate.Unlock()

	snapshot, err := controller.repository.Load(ctx)
	if err != nil {
		return SaveSettingsResult{}, safeRepositoryError("load settings for save", err)
	}
	if snapshot.Revision != expectedRevision {
		return SaveSettingsResult{}, agent.NewError(agent.ErrorConflict, "save settings", errors.New("settings revision is stale; reload before saving"))
	}
	merged, err := mergePatch(snapshot.Values, patch)
	if err != nil {
		return SaveSettingsResult{}, err
	}
	if err := config.ValidateDraft(merged); err != nil {
		return SaveSettingsResult{}, validationError(err)
	}
	saved, err := controller.repository.Save(ctx, expectedRevision, merged)
	if err != nil {
		return SaveSettingsResult{}, safeRepositoryError("save settings", err)
	}
	return SaveSettingsResult{
		View:           viewFromSnapshot(saved),
		Revision:       saved.Revision,
		PendingChanges: true,
	}, nil
}

func (controller *Controller) SaveSettings(ctx context.Context, request SaveSettingsRequest) (SaveSettingsResult, error) {
	return controller.Save(ctx, request.ExpectedRevision, request.Patch)
}

func settingsFromPatch(patch SettingsPatch) config.Settings {
	if !reflect.ValueOf(patch.Draft).IsZero() {
		return patch.Draft
	}
	if !reflect.ValueOf(patch.Settings).IsZero() {
		return patch.Settings
	}
	return patch.Values
}

func mergePatch(current config.Settings, patch SettingsPatch) (config.Settings, error) {
	merged := settingsFromPatch(patch)
	merged.LLMAPIKey = current.LLMAPIKey
	merged.FallbackAPIKey = current.FallbackAPIKey
	merged.LangSmithAPIKey = current.LangSmithAPIKey
	if err := mergeSecret(&merged.LLMAPIKey, patch.Secrets.LLMAPIKey, "WAZZAP_LLM_API_KEY"); err != nil {
		return config.Settings{}, err
	}
	if err := mergeSecret(&merged.FallbackAPIKey, patch.Secrets.FallbackAPIKey, "WAZZAP_LLM_FALLBACK_API_KEY"); err != nil {
		return config.Settings{}, err
	}
	if err := mergeSecret(&merged.LangSmithAPIKey, patch.Secrets.LangSmithAPIKey, "LANGSMITH_API_KEY"); err != nil {
		return config.Settings{}, err
	}
	return merged, nil
}

func mergeSecret(destination *string, update SecretUpdate, field string) error {
	switch update.Action {
	case "", SecretKeep:
		return nil
	case SecretReplace:
		if strings.TrimSpace(update.Value) == "" {
			return agent.NewError(agent.ErrorInvalidArgument, "save settings", fmt.Errorf("%s replacement must be non-empty", field))
		}
		*destination = update.Value
	case SecretClear:
		*destination = ""
	default:
		return agent.NewError(agent.ErrorInvalidArgument, "save settings", fmt.Errorf("%s secret action is invalid", field))
	}
	return nil
}

func validationResult(settings config.Settings) ValidationResult {
	draft := config.DraftReadinessIssues(settings)
	session := config.SessionReadinessIssues(settings)
	agentIssues := config.AgentReadinessIssues(settings)
	return ValidationResult{
		Valid:            len(draft) == 0,
		Readiness:        draft,
		SessionReadiness: session,
		AgentReadiness:   agentIssues,
	}
}

func viewFromSnapshot(snapshot SettingsSnapshot) SettingsView {
	return SettingsView{
		Values:           snapshot.Values.Public(),
		Revision:         snapshot.Revision,
		UpdatedAt:        snapshot.UpdatedAt,
		Readiness:        config.DraftReadinessIssues(snapshot.Values),
		SessionReadiness: config.SessionReadinessIssues(snapshot.Values),
		AgentReadiness:   config.AgentReadinessIssues(snapshot.Values),
	}
}

func validationError(err error) error {
	if err == nil {
		return nil
	}
	return agent.NewError(agent.ErrorInvalidArgument, "validate settings", errors.New(err.Error()))
}

func controllerError(op string, cause error) error {
	return agent.NewError(agent.ErrorUnavailable, op, cause)
}

func safeRepositoryError(op string, err error) error {
	code := agent.CodeOf(err)
	switch code {
	case agent.ErrorConflict:
		return agent.NewError(agent.ErrorConflict, op, errors.New("settings revision conflict"))
	case agent.ErrorInvalidArgument, agent.ErrorIntegrityFailure:
		return agent.NewError(code, op, errors.New("settings data is invalid"))
	default:
		return agent.NewError(agent.ErrorStorageFailure, op, errors.New("settings storage operation failed"))
	}
}
