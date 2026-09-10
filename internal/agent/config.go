package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	InitialConfigVersion ConfigVersion = 1
	MaxPromptBytes                     = 16 * 1024
	MaxModelNameBytes                  = 256
	MaxOutputTokens                    = 65_536
)

type ConfigVersion uint64

type ModelConfig struct {
	ProviderID      identity.ProviderID
	Model           string
	MaxOutputTokens uint32
}

type PromptOverrideMode uint8

const (
	PromptAppend PromptOverrideMode = iota + 1
	PromptReplace
)

type PromptOverride struct {
	Mode PromptOverrideMode
	Text string
}

type PermissionConfig struct {
	PolicyID          identity.PolicyID
	Revision          uint64
	ModelCapabilities CapabilitySet
}

// ModelToolCapabilities is the intentionally small set that a chat owner may
// opt in for the model. Human-command and administrative capabilities never
// cross the Agent/model boundary. Message deletion is deliberately excluded:
// it stays available to a future explicitly-authorized human feature, but is
// not a model privilege in Part 3.
func (permission PermissionConfig) ModelToolCapabilities() CapabilitySet {
	return CapabilitySet{values: permission.ModelCapabilities.Values()}
}

func (permission PermissionConfig) Validate() error {
	if permission.PolicyID.IsZero() || permission.Revision == 0 {
		return NewError(ErrorInvalidArgument, "validate permission config", fmt.Errorf("permission policy reference is required"))
	}
	for _, capability := range permission.ModelCapabilities.Values() {
		switch capability {
		case "message.react", "message.mark-read", "chat.presence":
		default:
			return NewError(ErrorInvalidArgument, "validate permission config", fmt.Errorf("model capability is not allowed"))
		}
	}
	return nil
}

type ConfigValues struct {
	Model          ModelConfig
	Prompt         string
	PromptOverride *PromptOverride
	Permission     PermissionConfig
}

type ConfigSnapshot struct {
	Version        ConfigVersion
	Model          ModelConfig
	Prompt         string
	PromptOverride *PromptOverride
	Permission     PermissionConfig
}

type ConfigStore interface {
	LoadOrCreate(context.Context, Key, ConfigValues) (ConfigSnapshot, error)
	Load(context.Context, Key) (ConfigSnapshot, error)
	CompareAndSwap(context.Context, Key, ConfigVersion, ConfigValues) (ConfigSnapshot, error)
}

type ConfigField uint8

const (
	ConfigFieldModel ConfigField = iota + 1
	ConfigFieldPrompt
	ConfigFieldPromptOverride
	ConfigFieldPermission
)

type ConfigChanged struct {
	Key           Key
	Previous      ConfigVersion
	Current       ConfigVersion
	ChangedFields []ConfigField
	OccurredAt    time.Time
}

type ConfigEventSink interface{ TryPublish(ConfigChanged) bool }

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type DiscardConfigEvents struct{}

func (DiscardConfigEvents) TryPublish(ConfigChanged) bool { return true }

type Config struct {
	key    Key
	store  ConfigStore
	events ConfigEventSink
	clock  Clock

	mu       sync.RWMutex
	snapshot ConfigSnapshot
	staleAt  ConfigVersion
}

func newConfig(
	ctx context.Context,
	key Key,
	defaults ConfigValues,
	store ConfigStore,
	events ConfigEventSink,
	clock Clock,
) (*Config, error) {
	if store == nil || events == nil || clock == nil {
		return nil, NewError(ErrorInvalidArgument, "create config", fmt.Errorf("store, events, and clock are required"))
	}
	if err := validateConfigValues(defaults); err != nil {
		return nil, err
	}
	snapshot, err := store.LoadOrCreate(ctx, key, cloneConfigValues(defaults))
	if err != nil {
		return nil, err
	}
	if err := validateConfigSnapshot(snapshot); err != nil {
		return nil, err
	}
	return &Config{
		key:      key,
		store:    store,
		events:   events,
		clock:    clock,
		snapshot: cloneConfigSnapshot(snapshot),
	}, nil
}

func (config *Config) Snapshot() ConfigSnapshot {
	config.mu.RLock()
	defer config.mu.RUnlock()
	return cloneConfigSnapshot(config.snapshot)
}

func (config *Config) Refresh(ctx context.Context) (ConfigSnapshot, error) {
	loaded, err := config.store.Load(ctx, config.key)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if err := validateConfigSnapshot(loaded); err != nil {
		return ConfigSnapshot{}, err
	}

	config.mu.Lock()
	defer config.mu.Unlock()
	if loaded.Version < config.snapshot.Version {
		return ConfigSnapshot{}, NewError(ErrorIntegrityFailure, "refresh config", fmt.Errorf("durable config version regressed"))
	}
	if loaded.Version == config.snapshot.Version && !configSnapshotsEqual(loaded, config.snapshot) {
		return ConfigSnapshot{}, NewError(ErrorIntegrityFailure, "refresh config", fmt.Errorf("config values changed without a version increment"))
	}
	if loaded.Version > config.snapshot.Version {
		config.snapshot = cloneConfigSnapshot(loaded)
	}
	if config.staleAt <= config.snapshot.Version {
		config.staleAt = 0
	}
	return cloneConfigSnapshot(config.snapshot), nil
}

func (config *Config) SetModel(ctx context.Context, expected ConfigVersion, value ModelConfig) (ConfigSnapshot, error) {
	return config.mutate(ctx, expected, []ConfigField{ConfigFieldModel}, func(values *ConfigValues) {
		values.Model = value
	})
}

func (config *Config) SetPrompt(ctx context.Context, expected ConfigVersion, value string) (ConfigSnapshot, error) {
	return config.mutate(ctx, expected, []ConfigField{ConfigFieldPrompt}, func(values *ConfigValues) {
		values.Prompt = value
	})
}

func (config *Config) SetPromptOverride(ctx context.Context, expected ConfigVersion, value PromptOverride) (ConfigSnapshot, error) {
	return config.mutate(ctx, expected, []ConfigField{ConfigFieldPromptOverride}, func(values *ConfigValues) {
		copyOverride := value
		values.PromptOverride = &copyOverride
	})
}

func (config *Config) ClearPromptOverride(ctx context.Context, expected ConfigVersion) (ConfigSnapshot, error) {
	return config.mutate(ctx, expected, []ConfigField{ConfigFieldPromptOverride}, func(values *ConfigValues) {
		values.PromptOverride = nil
	})
}

func (config *Config) SetPermission(ctx context.Context, expected ConfigVersion, value PermissionConfig) (ConfigSnapshot, error) {
	return config.mutate(ctx, expected, []ConfigField{ConfigFieldPermission}, func(values *ConfigValues) {
		values.Permission = value
	})
}

func (config *Config) mutate(
	ctx context.Context,
	expected ConfigVersion,
	fields []ConfigField,
	change func(*ConfigValues),
) (ConfigSnapshot, error) {
	if expected == 0 {
		return ConfigSnapshot{}, NewError(ErrorInvalidArgument, "mutate config", fmt.Errorf("expected version is required"))
	}

	config.mu.RLock()
	current := cloneConfigSnapshot(config.snapshot)
	config.mu.RUnlock()
	if current.Version != expected {
		return ConfigSnapshot{}, NewError(ErrorConflict, "mutate config", fmt.Errorf("stale config version"))
	}
	values := current.Values()
	change(&values)
	if err := validateConfigValues(values); err != nil {
		return ConfigSnapshot{}, err
	}
	committed, err := config.store.CompareAndSwap(ctx, config.key, expected, cloneConfigValues(values))
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if committed.Version != expected+1 || committed.Version == 0 {
		return ConfigSnapshot{}, NewError(ErrorIntegrityFailure, "mutate config", fmt.Errorf("store returned nonsequential version"))
	}
	if err := validateConfigSnapshot(committed); err != nil {
		return ConfigSnapshot{}, err
	}

	config.mu.Lock()
	if committed.Version > config.snapshot.Version {
		config.snapshot = cloneConfigSnapshot(committed)
	}
	if config.staleAt <= config.snapshot.Version {
		config.staleAt = 0
	}
	config.mu.Unlock()

	event := ConfigChanged{
		Key:           config.key,
		Previous:      expected,
		Current:       committed.Version,
		ChangedFields: append([]ConfigField(nil), fields...),
		OccurredAt:    config.clock.Now(),
	}
	config.events.TryPublish(event)
	return cloneConfigSnapshot(committed), nil
}

func (config *Config) markStale(version ConfigVersion) {
	config.mu.Lock()
	if version > config.staleAt {
		config.staleAt = version
	}
	config.mu.Unlock()
}

func (snapshot ConfigSnapshot) Values() ConfigValues {
	return ConfigValues{
		Model:          snapshot.Model,
		Prompt:         snapshot.Prompt,
		PromptOverride: clonePromptOverride(snapshot.PromptOverride),
		Permission:     clonePermissionConfig(snapshot.Permission),
	}
}

func validateConfigSnapshot(snapshot ConfigSnapshot) error {
	if snapshot.Version == 0 {
		return NewError(ErrorIntegrityFailure, "validate config snapshot", fmt.Errorf("version is zero"))
	}
	return validateConfigValues(snapshot.Values())
}

func validateConfigValues(values ConfigValues) error {
	if values.Model.ProviderID.IsZero() || strings.TrimSpace(values.Model.Model) == "" ||
		!utf8.ValidString(values.Model.Model) || len(values.Model.Model) > MaxModelNameBytes {
		return NewError(ErrorInvalidArgument, "validate config", fmt.Errorf("valid provider and model are required"))
	}
	if values.Model.MaxOutputTokens == 0 || values.Model.MaxOutputTokens > MaxOutputTokens {
		return NewError(ErrorInvalidArgument, "validate config", fmt.Errorf("max output tokens must be between 1 and %d", MaxOutputTokens))
	}
	if strings.TrimSpace(values.Prompt) == "" || !utf8.ValidString(values.Prompt) || len(values.Prompt) > MaxPromptBytes {
		return NewError(ErrorInvalidArgument, "validate config", fmt.Errorf("base prompt must be non-empty valid UTF-8 within %d bytes", MaxPromptBytes))
	}
	if values.PromptOverride != nil {
		if values.PromptOverride.Mode != PromptAppend && values.PromptOverride.Mode != PromptReplace {
			return NewError(ErrorInvalidArgument, "validate config", fmt.Errorf("invalid prompt override mode"))
		}
		if strings.TrimSpace(values.PromptOverride.Text) == "" || !utf8.ValidString(values.PromptOverride.Text) || len(values.PromptOverride.Text) > MaxPromptBytes {
			return NewError(ErrorInvalidArgument, "validate config", fmt.Errorf("prompt override must be non-empty valid UTF-8 within %d bytes", MaxPromptBytes))
		}
	}
	if err := values.Permission.Validate(); err != nil {
		return err
	}
	return nil
}

// ValidateConfigValues lets a durable adapter defend its write boundary even
// when a future caller bypasses the Agent facade. It does not authorize a
// change; actor checks stay outside Agent and storage.
func ValidateConfigValues(values ConfigValues) error { return validateConfigValues(values) }

func cloneConfigValues(values ConfigValues) ConfigValues {
	values.PromptOverride = clonePromptOverride(values.PromptOverride)
	values.Permission = clonePermissionConfig(values.Permission)
	return values
}

func cloneConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	snapshot.PromptOverride = clonePromptOverride(snapshot.PromptOverride)
	snapshot.Permission = clonePermissionConfig(snapshot.Permission)
	return snapshot
}

func clonePermissionConfig(value PermissionConfig) PermissionConfig {
	value.ModelCapabilities = CapabilitySet{values: value.ModelCapabilities.Values()}
	return value
}

func clonePromptOverride(value *PromptOverride) *PromptOverride {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func configSnapshotsEqual(left, right ConfigSnapshot) bool {
	if left.Version != right.Version || left.Model != right.Model || left.Prompt != right.Prompt ||
		left.Permission.PolicyID != right.Permission.PolicyID || left.Permission.Revision != right.Permission.Revision ||
		!capabilitySetsEqual(left.Permission.ModelCapabilities, right.Permission.ModelCapabilities) {
		return false
	}
	if left.PromptOverride == nil || right.PromptOverride == nil {
		return left.PromptOverride == nil && right.PromptOverride == nil
	}
	return *left.PromptOverride == *right.PromptOverride
}

func capabilitySetsEqual(left, right CapabilitySet) bool {
	leftValues, rightValues := left.Values(), right.Values()
	if len(leftValues) != len(rightValues) {
		return false
	}
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}
