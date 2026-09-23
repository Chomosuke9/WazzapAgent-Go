//go:build gui

package wails

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// SettingsValuesDTO is the public settings form value. It deliberately does
// not contain any secret value; configured is represented by the three
// boolean status fields below.
type SettingsValuesDTO struct {
	AssistantName string              `json:"assistantName"`
	BasePrompt    string              `json:"basePrompt"`
	ChatDefaults  config.ChatDefaults `json:"chatDefaults"`

	WhatsAppEnabled bool     `json:"whatsAppEnabled"`
	AgentEnabled    bool     `json:"agentEnabled"`
	OwnerJID        string   `json:"ownerJID"`
	ChatAllowlist   []string `json:"chatAllowlist"`

	LLMEndpoint      string `json:"llmEndpoint"`
	LLMModel         string `json:"llmModel"`
	LLMProviderID    string `json:"llmProviderID"`
	FallbackEndpoint string `json:"fallbackEndpoint"`

	LLMTimeout       string `json:"llmTimeout"`
	LLMConcurrency   uint32 `json:"llmConcurrency"`
	MaxOutputTokens  uint32 `json:"maxOutputTokens"`
	MaxResponseBytes uint32 `json:"maxResponseBytes"`

	HistoryWindow     uint32 `json:"historyWindow"`
	MaxContextBytes   uint32 `json:"maxContextBytes"`
	HistoryKeepLatest uint32 `json:"historyKeepLatest"`
	HistoryMaxAge     string `json:"historyMaxAge"`

	InboundQueue   uint32 `json:"inboundQueue"`
	InboundWorkers uint32 `json:"inboundWorkers"`
	CommandQueue   uint32 `json:"commandQueue"`
	CommandWorkers uint32 `json:"commandWorkers"`
	AIQueue        uint32 `json:"aiQueue"`
	AIWorkers      uint32 `json:"aiWorkers"`

	MessageDebounce string `json:"messageDebounce"`
	MessageBurstCap uint32 `json:"messageBurstCap"`

	AgentMaxLive             uint32 `json:"agentMaxLive"`
	AgentIdleTTL             string `json:"agentIdleTTL"`
	AgentConstructionTimeout string `json:"agentConstructionTimeout"`
	ConnectTimeout           string `json:"connectTimeout"`
	SendTimeout              string `json:"sendTimeout"`
	ShutdownTimeout          string `json:"shutdownTimeout"`

	PolicyID       string `json:"policyID"`
	PolicyRevision string `json:"policyRevision"`
	LogLevel       string `json:"logLevel"`
	LogFormat      string `json:"logFormat"`

	DataDir       string `json:"dataDir"`
	EnvFile       string `json:"envFile"`
	HTTPAddress   string `json:"httpAddress"`
	PairingOutput string `json:"pairingOutput"`
	NoColor       bool   `json:"noColor"`
	ForceColor    bool   `json:"forceColor"`
	StartOnLaunch bool   `json:"startOnLaunch"`

	// Identity is read-only bootstrap state. It is included so a complete
	// settings draft can be round-tripped without changing it to zero.
	TenantID  string `json:"tenantID"`
	AccountID string `json:"accountID"`

	LLMAPIKeyConfigured       bool `json:"llmAPIKeyConfigured"`
	FallbackAPIKeyConfigured  bool `json:"fallbackAPIKeyConfigured"`
	LangSmithAPIKeyConfigured bool `json:"langSmithAPIKeyConfigured"`
}

// PublicSettingsDTO and SettingsDTO are descriptive aliases for generated
// bindings and callers that use either term for the public form value.
type PublicSettingsDTO = SettingsValuesDTO
type SettingsDTO = SettingsValuesDTO

type ReadinessIssueDTO struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SettingsViewDTO struct {
	Values           SettingsValuesDTO   `json:"values"`
	Revision         string              `json:"revision"`
	UpdatedAt        string              `json:"updatedAt"`
	Readiness        []ReadinessIssueDTO `json:"readiness"`
	SessionReadiness []ReadinessIssueDTO `json:"sessionReadiness"`
	AgentReadiness   []ReadinessIssueDTO `json:"agentReadiness"`
}

type ValidationResultDTO struct {
	Valid            bool                `json:"valid"`
	Readiness        []ReadinessIssueDTO `json:"readiness"`
	SessionReadiness []ReadinessIssueDTO `json:"sessionReadiness"`
	AgentReadiness   []ReadinessIssueDTO `json:"agentReadiness"`
}

type SaveSettingsResultDTO struct {
	View           SettingsViewDTO `json:"view"`
	Revision       string          `json:"revision"`
	PendingChanges bool            `json:"pendingChanges"`
}

type FieldDescriptorDTO struct {
	Key       string `json:"key"`
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Sensitive bool   `json:"sensitive"`
	ReadOnly  bool   `json:"readOnly"`
	CLIOnly   bool   `json:"cliOnly"`
	Default   string `json:"default"`
}

type SecretUpdateDTO struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

type SecretPatchDTO struct {
	LLMAPIKey       SecretUpdateDTO `json:"llmAPIKey"`
	FallbackAPIKey  SecretUpdateDTO `json:"fallbackAPIKey"`
	LangSmithAPIKey SecretUpdateDTO `json:"langSmithAPIKey"`
}

// SettingsPatchDTO accepts Draft as its canonical field. Settings and Values
// remain harmless aliases for callers that use those names in their form
// model; the first non-empty value wins in that order.
type SettingsPatchDTO struct {
	Draft    SettingsValuesDTO `json:"draft"`
	Settings SettingsValuesDTO `json:"settings"`
	Values   SettingsValuesDTO `json:"values"`
	Secrets  SecretPatchDTO    `json:"secrets"`
}

type SaveSettingsRequestDTO struct {
	ExpectedRevision string           `json:"expectedRevision"`
	Patch            SettingsPatchDTO `json:"patch"`
}

type AgentRuntimeStatusDTO struct {
	State          string `json:"state"`
	SavedRevision  string `json:"savedRevision"`
	ActiveRevision string `json:"activeRevision"`
	PendingChanges bool   `json:"pendingChanges"`
	WhatsAppState  string `json:"whatsAppState"`
	ErrorCode      string `json:"errorCode,omitempty"`
	OperationID    string `json:"operationID,omitempty"`
}

type ApplyAgentSettingsRequestDTO struct {
	ExpectedRevision string `json:"expectedRevision"`
}

type BeginWhatsAppPairingRequestDTO struct {
	Method string `json:"method"`
	Phone  string `json:"phone,omitempty"`
}

type WhatsAppPairingDTO struct {
	Method        string `json:"method"`
	Code          string `json:"code,omitempty"`
	QRCodeDataURL string `json:"qrCodeDataURL,omitempty"`
	Generation    uint64 `json:"generation"`
	ExpiresAt     string `json:"expiresAt"`
}

type WhatsAppSessionStatusDTO struct {
	BindingState      string              `json:"bindingState"`
	RuntimeState      string              `json:"runtimeState"`
	SessionPresent    bool                `json:"sessionPresent"`
	AgentActive       bool                `json:"agentActive"`
	WhatsAppAccountID string              `json:"whatsAppAccountID,omitempty"`
	OperationID       string              `json:"operationID,omitempty"`
	Pairing           *WhatsAppPairingDTO `json:"pairing,omitempty"`
	ErrorCode         string              `json:"errorCode,omitempty"`
}

type WhatsAppSessionOperationDTO struct {
	OperationID string                   `json:"operationID"`
	Status      WhatsAppSessionStatusDTO `json:"status"`
}

type WhatsAppSessionEventDTO struct {
	OperationID string                   `json:"operationID"`
	Status      WhatsAppSessionStatusDTO `json:"status"`
}

type SettingsView = SettingsViewDTO
type ValidationResult = ValidationResultDTO
type SaveSettingsResult = SaveSettingsResultDTO

func whatsappSessionStatusDTO(status control.SessionStatus) WhatsAppSessionStatusDTO {
	dto := WhatsAppSessionStatusDTO{
		BindingState: string(status.BindingState), RuntimeState: string(status.RuntimeState),
		SessionPresent: status.SessionPresent, WhatsAppAccountID: status.WhatsAppAccountID,
		OperationID: status.OperationID, ErrorCode: string(status.ErrorCode),
	}
	if status.Pairing != nil {
		dto.Pairing = &WhatsAppPairingDTO{
			Method: string(status.Pairing.Method), Code: status.Pairing.Code,
			QRCodeDataURL: status.Pairing.QRCodeDataURL, Generation: status.Pairing.Generation,
			ExpiresAt: formatTime(status.Pairing.ExpiresAt),
		}
	}
	return dto
}

func whatsappSessionOperationDTO(operation control.SessionOperation) WhatsAppSessionOperationDTO {
	return WhatsAppSessionOperationDTO{OperationID: operation.OperationID, Status: whatsappSessionStatusDTO(operation.Status)}
}

func agentRuntimeStatusDTO(status control.AgentRuntimeStatus) AgentRuntimeStatusDTO {
	return AgentRuntimeStatusDTO{
		State: string(status.State), SavedRevision: strconv.FormatUint(status.SavedRevision, 10),
		ActiveRevision: strconv.FormatUint(status.ActiveRevision, 10), PendingChanges: status.PendingChanges,
		WhatsAppState: status.WhatsAppState, ErrorCode: string(status.ErrorCode), OperationID: status.OperationID,
	}
}

func settingsViewDTO(view control.SettingsView) SettingsViewDTO {
	return SettingsViewDTO{
		Values:           publicSettingsDTO(view.Values),
		Revision:         strconv.FormatUint(view.Revision, 10),
		UpdatedAt:        formatTime(view.UpdatedAt),
		Readiness:        readinessDTO(view.Readiness),
		SessionReadiness: readinessDTO(view.SessionReadiness),
		AgentReadiness:   readinessDTO(view.AgentReadiness),
	}
}

func validationResultDTO(result control.ValidationResult) ValidationResultDTO {
	return ValidationResultDTO{
		Valid:            result.Valid,
		Readiness:        readinessDTO(result.Readiness),
		SessionReadiness: readinessDTO(result.SessionReadiness),
		AgentReadiness:   readinessDTO(result.AgentReadiness),
	}
}

func saveSettingsResultDTO(result control.SaveSettingsResult) SaveSettingsResultDTO {
	return SaveSettingsResultDTO{
		View:           settingsViewDTO(result.View),
		Revision:       strconv.FormatUint(result.Revision, 10),
		PendingChanges: result.PendingChanges,
	}
}

func publicSettingsDTO(values config.PublicSettings) SettingsValuesDTO {
	settings := values.Settings
	return SettingsValuesDTO{
		AssistantName: settings.AssistantName, BasePrompt: settings.BasePrompt,
		ChatDefaults:    settings.ChatDefaults,
		WhatsAppEnabled: settings.WhatsAppEnabled, AgentEnabled: settings.AgentEnabled,
		OwnerJID: settings.OwnerJID, ChatAllowlist: append([]string(nil), settings.ChatAllowlist...),
		LLMEndpoint: settings.LLMEndpoint, LLMModel: settings.LLMModel, LLMProviderID: settings.LLMProviderID,
		FallbackEndpoint: settings.FallbackEndpoint,
		LLMTimeout:       settings.LLMTimeout.String(), LLMConcurrency: settings.LLMConcurrency,
		MaxOutputTokens: settings.MaxOutputTokens, MaxResponseBytes: settings.MaxResponseBytes,
		HistoryWindow: settings.HistoryWindow, MaxContextBytes: settings.MaxContextBytes,
		HistoryKeepLatest: settings.HistoryKeepLatest, HistoryMaxAge: settings.HistoryMaxAge.String(),
		InboundQueue: settings.InboundQueue, InboundWorkers: settings.InboundWorkers,
		CommandQueue: settings.CommandQueue, CommandWorkers: settings.CommandWorkers,
		AIQueue: settings.AIQueue, AIWorkers: settings.AIWorkers,
		MessageDebounce: settings.MessageDebounce.String(), MessageBurstCap: settings.MessageBurstCap,
		AgentMaxLive: settings.AgentMaxLive, AgentIdleTTL: settings.AgentIdleTTL.String(),
		AgentConstructionTimeout: settings.AgentConstructionTimeout.String(), ConnectTimeout: settings.ConnectTimeout.String(),
		SendTimeout: settings.SendTimeout.String(), ShutdownTimeout: settings.ShutdownTimeout.String(),
		PolicyID: settings.PolicyID, PolicyRevision: strconv.FormatUint(settings.PolicyRevision, 10),
		LogLevel: settings.LogLevel, LogFormat: settings.LogFormat,
		DataDir: settings.DataDir, EnvFile: settings.EnvFile, HTTPAddress: settings.HTTPAddress,
		PairingOutput: settings.PairingOutput, NoColor: settings.NoColor, ForceColor: settings.ForceColor,
		StartOnLaunch: settings.StartOnLaunch, TenantID: settings.TenantID.String(), AccountID: settings.AccountID.String(),
		LLMAPIKeyConfigured: values.LLMAPIKeyConfigured, FallbackAPIKeyConfigured: values.FallbackAPIKeyConfigured,
		LangSmithAPIKeyConfigured: values.LangSmithAPIKeyConfigured,
	}
}

func readinessDTO(issues []config.ReadinessIssue) []ReadinessIssueDTO {
	if len(issues) == 0 {
		return nil
	}
	result := make([]ReadinessIssueDTO, len(issues))
	for i, issue := range issues {
		result[i] = ReadinessIssueDTO{Field: issue.Field, Code: issue.Code, Message: issue.Message}
	}
	return result
}

func fieldDuration(value, field string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, agent.NewError(agent.ErrorInvalidArgument, "decode settings", errors.New(field+" duration is invalid"))
	}
	return duration, nil
}

func settingsFromDTO(values SettingsValuesDTO) (config.Settings, error) {
	var settings config.Settings
	var err error
	settings.LLMTimeout, err = fieldDuration(values.LLMTimeout, "llmTimeout")
	if err != nil {
		return config.Settings{}, err
	}
	settings.HistoryMaxAge, err = fieldDuration(values.HistoryMaxAge, "historyMaxAge")
	if err != nil {
		return config.Settings{}, err
	}
	settings.MessageDebounce, err = fieldDuration(values.MessageDebounce, "messageDebounce")
	if err != nil {
		return config.Settings{}, err
	}
	settings.AgentIdleTTL, err = fieldDuration(values.AgentIdleTTL, "agentIdleTTL")
	if err != nil {
		return config.Settings{}, err
	}
	settings.AgentConstructionTimeout, err = fieldDuration(values.AgentConstructionTimeout, "agentConstructionTimeout")
	if err != nil {
		return config.Settings{}, err
	}
	settings.ConnectTimeout, err = fieldDuration(values.ConnectTimeout, "connectTimeout")
	if err != nil {
		return config.Settings{}, err
	}
	settings.SendTimeout, err = fieldDuration(values.SendTimeout, "sendTimeout")
	if err != nil {
		return config.Settings{}, err
	}
	settings.ShutdownTimeout, err = fieldDuration(values.ShutdownTimeout, "shutdownTimeout")
	if err != nil {
		return config.Settings{}, err
	}
	settings.PolicyRevision, err = parseUint(values.PolicyRevision, "policyRevision")
	if err != nil {
		return config.Settings{}, err
	}
	if values.TenantID != "" {
		settings.TenantID, err = identity.ParseTenantID(values.TenantID)
		if err != nil {
			return config.Settings{}, agent.NewError(agent.ErrorInvalidArgument, "decode settings", errors.New("tenant ID is invalid"))
		}
	}
	if values.AccountID != "" {
		settings.AccountID, err = identity.ParseAccountID(values.AccountID)
		if err != nil {
			return config.Settings{}, agent.NewError(agent.ErrorInvalidArgument, "decode settings", errors.New("account ID is invalid"))
		}
	}
	settings.AssistantName, settings.BasePrompt = values.AssistantName, values.BasePrompt
	settings.ChatDefaults = values.ChatDefaults
	settings.WhatsAppEnabled, settings.AgentEnabled = values.WhatsAppEnabled, values.AgentEnabled
	settings.OwnerJID, settings.ChatAllowlist = values.OwnerJID, append([]string(nil), values.ChatAllowlist...)
	settings.LLMEndpoint, settings.LLMModel, settings.LLMProviderID = values.LLMEndpoint, values.LLMModel, values.LLMProviderID
	settings.FallbackEndpoint = values.FallbackEndpoint
	settings.LLMConcurrency, settings.MaxOutputTokens, settings.MaxResponseBytes = values.LLMConcurrency, values.MaxOutputTokens, values.MaxResponseBytes
	settings.HistoryWindow, settings.MaxContextBytes, settings.HistoryKeepLatest = values.HistoryWindow, values.MaxContextBytes, values.HistoryKeepLatest
	settings.InboundQueue, settings.InboundWorkers, settings.CommandQueue, settings.CommandWorkers = values.InboundQueue, values.InboundWorkers, values.CommandQueue, values.CommandWorkers
	settings.AIQueue, settings.AIWorkers, settings.MessageBurstCap = values.AIQueue, values.AIWorkers, values.MessageBurstCap
	settings.AgentMaxLive = values.AgentMaxLive
	settings.PolicyID, settings.LogLevel, settings.LogFormat = values.PolicyID, values.LogLevel, values.LogFormat
	settings.DataDir, settings.EnvFile, settings.HTTPAddress, settings.PairingOutput = values.DataDir, values.EnvFile, values.HTTPAddress, values.PairingOutput
	settings.NoColor, settings.ForceColor, settings.StartOnLaunch = values.NoColor, values.ForceColor, values.StartOnLaunch
	return settings, nil
}

func parseUint(value, field string) (uint64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, agent.NewError(agent.ErrorInvalidArgument, "decode settings", errors.New(field+" must be an unsigned integer"))
	}
	return parsed, nil
}

func patchFromDTO(patch SettingsPatchDTO) (control.SettingsPatch, error) {
	values := patch.Draft
	if reflect.ValueOf(values).IsZero() {
		values = patch.Settings
	}
	if reflect.ValueOf(values).IsZero() {
		values = patch.Values
	}
	draft, err := settingsFromDTO(values)
	if err != nil {
		return control.SettingsPatch{}, err
	}
	return control.SettingsPatch{Draft: draft, Secrets: control.SecretPatch{
		LLMAPIKey:       control.SecretUpdate{Action: control.SecretAction(patch.Secrets.LLMAPIKey.Action), Value: patch.Secrets.LLMAPIKey.Value},
		FallbackAPIKey:  control.SecretUpdate{Action: control.SecretAction(patch.Secrets.FallbackAPIKey.Action), Value: patch.Secrets.FallbackAPIKey.Value},
		LangSmithAPIKey: control.SecretUpdate{Action: control.SecretAction(patch.Secrets.LangSmithAPIKey.Action), Value: patch.Secrets.LangSmithAPIKey.Value},
	}}, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
