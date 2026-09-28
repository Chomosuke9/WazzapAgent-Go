package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	InitialConfigVersion   ConfigVersion = 1
	MaxPromptBytes                       = 16 * 1024
	MaxModelNameBytes                    = 256
	MaxTriggerPatternBytes               = 512
	MaxOutputTokens                      = 65_536
)

type ConfigVersion uint64

type ModelConfig struct {
	ProviderID      identity.ProviderID
	Model           string
	MaxOutputTokens uint32
}

type PromptOverrideMode uint8

const (
	// PromptAppend sends the text in <prompt_override> alongside the chat prompt in <additional>.
	PromptAppend PromptOverrideMode = iota + 1
	// PromptReplace sends the text in <prompt_override> and leaves <additional> empty.
	PromptReplace
)

type PromptOverride struct {
	Mode PromptOverrideMode
	Text string
}

type PermissionConfig struct {
	PolicyID        identity.PolicyID
	Revision        uint64
	ModerationLevel ModerationLevel
}

// TriggerConfig defines the group-message conditions that may invoke the Agent.
// Direct chats remain eligible without an invocation trigger.
type TriggerConfig struct {
	Mention     bool
	Name        bool
	Reply       bool
	NameRegex   bool
	NamePattern string
	// Smart asks a TypeSafe judgment whether a group message nobody
	// mentioned, replied or named the assistant in is still meant for it.
	// Matches ignores it; the policy gate runs the judgment.
	Smart bool
}

func DefaultTriggerConfig() TriggerConfig {
	return TriggerConfig{Mention: true, Reply: true}
}

func (triggers TriggerConfig) Validate() error {
	if !utf8.ValidString(triggers.NamePattern) || len(triggers.NamePattern) > MaxTriggerPatternBytes {
		return Errorf(ErrorInvalidArgument, "validate trigger config", "name trigger pattern must be valid UTF-8 and at most %d bytes", MaxTriggerPatternBytes)
	}
	if triggers.NameRegex {
		if triggers.Name && strings.TrimSpace(triggers.NamePattern) == "" {
			return Errorf(ErrorInvalidArgument, "validate trigger config", "a name pattern is required when regex mode is enabled")
		}
		if triggers.NamePattern != "" {
			if _, err := regexp.Compile(triggers.NamePattern); err != nil {
				return NewError(ErrorInvalidArgument, "validate trigger config", err)
			}
		}
	}
	return nil
}

func (triggers TriggerConfig) Matches(mentionsBot, repliedToBot bool, text, assistantName string) bool {
	if triggers.Mention && mentionsBot || triggers.Reply && repliedToBot {
		return true
	}
	if !triggers.Name {
		return false
	}
	if triggers.NameRegex {
		if strings.TrimSpace(triggers.NamePattern) == "" {
			return false
		}
		pattern, err := regexp.Compile(triggers.NamePattern)
		return err == nil && pattern.MatchString(text)
	}
	name := strings.TrimSpace(assistantName)
	return name != "" && strings.Contains(strings.ToLower(text), strings.ToLower(name))
}

type ModerationLevel uint8

const (
	ModerationNone ModerationLevel = iota
	ModerationDelete
	ModerationDeleteMute
	ModerationDeleteMuteKick
)

func (level ModerationLevel) Valid() bool { return level <= ModerationDeleteMuteKick }

// ModelToolCapabilities is the complete model-output capability set: the
// react_to_message and send_sticker tools. Commands the model requests in reply_message are not
// capabilities; the command registry's permission expression decides them.
func (permission PermissionConfig) ModelToolCapabilities() CapabilitySet {
	result, _ := NewCapabilitySet(CapabilityMessageReact, CapabilityMessageSticker)
	return result
}

func (permission PermissionConfig) Validate() error {
	if permission.PolicyID.IsZero() || permission.Revision == 0 {
		return Errorf(ErrorInvalidArgument, "validate permission config", "permission policy reference is required")
	}
	if !permission.ModerationLevel.Valid() {
		return Errorf(ErrorInvalidArgument, "validate permission config", "moderation level must be 0-3")
	}
	return nil
}

type ConfigValues struct {
	Model          ModelConfig
	Prompt         string
	PromptOverride *PromptOverride
	Permission     PermissionConfig
	Triggers       TriggerConfig
}

type ConfigSnapshot struct {
	Version        ConfigVersion
	Model          ModelConfig
	Prompt         string
	PromptOverride *PromptOverride
	Permission     PermissionConfig
	Triggers       TriggerConfig
}

type ConfigStore interface {
	LoadOrCreate(context.Context, Key, ConfigValues) (ConfigSnapshot, error)
	Load(context.Context, Key) (ConfigSnapshot, error)
	CompareAndSwap(context.Context, Key, ConfigVersion, ConfigValues) (ConfigSnapshot, error)
}

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Config reads and writes one chat's durable config. It keeps no copy: every
// read goes to the store, and writes are compare-and-swap on the version.
type Config struct {
	key   Key
	store ConfigStore
}

func newConfig(ctx context.Context, key Key, defaults ConfigValues, store ConfigStore) (*Config, error) {
	if store == nil {
		return nil, NewError(ErrorInvalidArgument, "create config", fmt.Errorf("store is required"))
	}
	if err := validateConfigValues(defaults); err != nil {
		return nil, NewError(ErrorInvalidArgument, "create config", err)
	}
	snapshot, err := store.LoadOrCreate(ctx, key, cloneConfigValues(defaults))
	if err != nil {
		return nil, NewError(ErrorStorageFailure, "create config", err)
	}
	if err := validateConfigSnapshot(snapshot); err != nil {
		return nil, NewError(ErrorIntegrityFailure, "create config", err)
	}
	return &Config{key: key, store: store}, nil
}

// Refresh returns the chat's current durable config.
func (config *Config) Refresh(ctx context.Context) (ConfigSnapshot, error) {
	loaded, err := config.store.Load(ctx, config.key)
	if err != nil {
		return ConfigSnapshot{}, NewError(ErrorStorageFailure, "refresh config", err)
	}
	if err := validateConfigSnapshot(loaded); err != nil {
		return ConfigSnapshot{}, NewError(ErrorIntegrityFailure, "refresh config", err)
	}
	return loaded, nil
}

func (config *Config) SetModel(ctx context.Context, expected ConfigVersion, value ModelConfig) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) { values.Model = value })
}

func (config *Config) SetPrompt(ctx context.Context, expected ConfigVersion, value string) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) { values.Prompt = value })
}

func (config *Config) SetPromptOverride(ctx context.Context, expected ConfigVersion, value PromptOverride) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) {
		copyOverride := value
		values.PromptOverride = &copyOverride
	})
}

func (config *Config) ClearPromptOverride(ctx context.Context, expected ConfigVersion) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) { values.PromptOverride = nil })
}

func (config *Config) SetPermission(ctx context.Context, expected ConfigVersion, value PermissionConfig) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) { values.Permission = value })
}

func (config *Config) SetTriggers(ctx context.Context, expected ConfigVersion, value TriggerConfig) (ConfigSnapshot, error) {
	return config.Update(ctx, expected, func(values *ConfigValues) { values.Triggers = value })
}

// Update applies change to the values at version expected and commits the
// result as the next version. It fails with ErrorConflict if the stored
// version is no longer expected.
func (config *Config) Update(ctx context.Context, expected ConfigVersion, change func(*ConfigValues)) (ConfigSnapshot, error) {
	if expected == 0 || change == nil {
		return ConfigSnapshot{}, NewError(ErrorInvalidArgument, "update config", fmt.Errorf("expected version and change are required"))
	}
	current, err := config.Refresh(ctx)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if current.Version != expected {
		return ConfigSnapshot{}, NewError(ErrorConflict, "update config", fmt.Errorf("stale config version"))
	}
	values := current.Values()
	change(&values)
	if err := validateConfigValues(values); err != nil {
		return ConfigSnapshot{}, NewError(ErrorInvalidArgument, "update config", err)
	}
	committed, err := config.store.CompareAndSwap(ctx, config.key, expected, cloneConfigValues(values))
	if err != nil {
		return ConfigSnapshot{}, NewError(ErrorStorageFailure, "update config", err)
	}
	if err := validateConfigSnapshot(committed); err != nil {
		return ConfigSnapshot{}, NewError(ErrorIntegrityFailure, "update config", err)
	}
	return committed, nil
}

func (snapshot ConfigSnapshot) Values() ConfigValues {
	return ConfigValues{
		Model:          snapshot.Model,
		Prompt:         snapshot.Prompt,
		PromptOverride: clonePromptOverride(snapshot.PromptOverride),
		Permission:     clonePermissionConfig(snapshot.Permission),
		Triggers:       snapshot.Triggers,
	}
}

func validateConfigSnapshot(snapshot ConfigSnapshot) error {
	if snapshot.Version == 0 {
		return Errorf(ErrorIntegrityFailure, "validate config snapshot", "version is zero")
	}
	if err := validateConfigValues(snapshot.Values()); err != nil {
		return NewError(ErrorIntegrityFailure, "validate config snapshot", err)
	}
	return nil
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
		return NewError(ErrorInvalidArgument, "validate config", err)
	}
	if err := values.Triggers.Validate(); err != nil {
		return NewError(ErrorInvalidArgument, "validate config", err)
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

func clonePermissionConfig(value PermissionConfig) PermissionConfig {
	return value
}

func clonePromptOverride(value *PromptOverride) *PromptOverride {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
