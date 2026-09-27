package control

import (
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

// SettingsSnapshot is the typed persistence boundary used by the controller.
// Secret fields may be present here, but are never returned by the public view.
type SettingsSnapshot struct {
	Revision  uint64
	Values    config.Settings
	UpdatedAt time.Time
}

// SecretAction controls one secret field in a save request. An omitted action
// is Keep, which allows the UI to submit its public draft without echoing a
// configured secret back to the backend.
type SecretAction string

const (
	SecretKeep    SecretAction = "keep"
	SecretReplace SecretAction = "replace"
	SecretClear   SecretAction = "clear"
)

// SecretUpdate is intentionally separate from config.Settings. The latter is
// a complete backend value and must never be populated from a masked UI value.
type SecretUpdate struct {
	Action SecretAction
	Value  string
}

// SecretPatch contains the only secret mutations accepted by SaveSettings.
type SecretPatch struct {
	LLMAPIKey       SecretUpdate
	FallbackAPIKey  SecretUpdate
	LangSmithAPIKey SecretUpdate
}

// SettingsPatch is a complete public draft plus secret actions.
type SettingsPatch struct {
	Draft   config.Settings
	Secrets SecretPatch
}

// SettingsView is safe to cross a UI boundary. It contains configured flags,
// never secret values, and readiness information for the form.
type SettingsView struct {
	Values           config.PublicSettings
	Revision         uint64
	UpdatedAt        time.Time
	Readiness        []config.ReadinessIssue
	SessionReadiness []config.ReadinessIssue
	AgentReadiness   []config.ReadinessIssue
}

type ValidationResult struct {
	Valid            bool
	Readiness        []config.ReadinessIssue
	SessionReadiness []config.ReadinessIssue
	AgentReadiness   []config.ReadinessIssue
}

type SaveSettingsResult struct {
	View           SettingsView
	Revision       uint64
	PendingChanges bool
}
