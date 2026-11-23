package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	// SubagentBackendService hands tasks to a WazzapSubAgents service.
	SubagentBackendService = "service"
	// SubagentBackendMetaAI hands tasks to Meta AI over the bot's own
	// WhatsApp account. It generates and edits images, looks at images and
	// searches the internet, answering with text and images.
	SubagentBackendMetaAI = "metaai"
)

// MetaAI reports whether tasks go to Meta AI.
func (settings SubagentSettings) MetaAI() bool {
	return strings.EqualFold(strings.TrimSpace(settings.Backend), SubagentBackendMetaAI)
}

// Configured reports whether there is a sub-agent at all.
func (settings SubagentSettings) Configured() bool {
	return settings.MetaAI() || strings.TrimSpace(settings.URL) != ""
}

// normalized trims the values and fills in the defaults.
func (settings SubagentSettings) normalized() SubagentSettings {
	settings.Backend = strings.ToLower(strings.TrimSpace(settings.Backend))
	if settings.Backend == "" {
		settings.Backend = SubagentBackendService
	}
	settings.URL = strings.TrimRight(strings.TrimSpace(settings.URL), "/")
	settings.APIToken = strings.TrimSpace(settings.APIToken)
	settings.WebhookAddress = nonempty(strings.TrimSpace(settings.WebhookAddress), defaultSubagentWebhookAddress)
	settings.WebhookToken = strings.TrimSpace(settings.WebhookToken)
	settings.WebhookURL = strings.TrimSpace(settings.WebhookURL)
	if settings.WebhookURL == "" {
		host, port, _ := net.SplitHostPort(settings.WebhookAddress)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		settings.WebhookURL = "http://" + net.JoinHostPort(host, port) + "/subagent/callback"
	}
	if settings.WaitTimeout <= 0 {
		settings.WaitTimeout = defaultSubagentWaitTimeout
	}
	if settings.MaxWait <= 0 {
		settings.MaxWait = defaultSubagentMaxWait
	}
	return settings
}

// WebhookLoopback reports whether the callback server listens on loopback
// only.
func (settings SubagentSettings) WebhookLoopback() bool {
	host, _, err := net.SplitHostPort(settings.normalized().WebhookAddress)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// issue is the first problem with a configured sub-agent, or nil. Nothing
// about the service is checked while no URL is set or Meta AI is used.
func (settings SubagentSettings) issue() *ReadinessIssue {
	normalized := settings.normalized()
	if normalized.Backend != SubagentBackendService && normalized.Backend != SubagentBackendMetaAI {
		return &ReadinessIssue{Field: "SUBAGENT_BACKEND", Code: "invalid", Message: "sub-agent backend must be service or metaai"}
	}
	if normalized.MetaAI() || normalized.URL == "" {
		return nil
	}
	for _, field := range []struct{ key, value string }{{"SUBAGENT_URL", normalized.URL}, {"SUBAGENT_WEBHOOK_URL", normalized.WebhookURL}} {
		if err := validateServiceURL(field.value); err != nil {
			return &ReadinessIssue{Field: field.key, Code: "invalid", Message: "sub-agent URL is invalid"}
		}
	}
	if err := validateHTTPAddress(normalized.WebhookAddress); err != nil {
		return &ReadinessIssue{Field: "SUBAGENT_WEBHOOK_ADDRESS", Code: "invalid", Message: "sub-agent webhook address is invalid"}
	}
	if !settings.WebhookLoopback() && normalized.WebhookToken == "" {
		// A callback can make the bot send a report and files to a chat.
		return &ReadinessIssue{Field: "SUBAGENT_WEBHOOK_TOKEN", Code: "required", Message: "a webhook token is required when the sub-agent webhook listens beyond loopback"}
	}
	if normalized.MaxWait < normalized.WaitTimeout {
		return &ReadinessIssue{Field: "SUBAGENT_MAX_WAIT", Code: "invalid", Message: "the maximum wait must not be shorter than the wait timeout"}
	}
	if normalized.MaxWait > 48*time.Hour {
		return &ReadinessIssue{Field: "SUBAGENT_MAX_WAIT", Code: "invalid", Message: "the maximum wait must be at most 48 hours"}
	}
	return nil
}

func validateServiceURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("must be an absolute http or https URL without credentials")
	}
	return nil
}
