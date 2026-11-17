package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

// normalized trims the values and fills in the defaults.
func (settings SubagentSettings) normalized() SubagentSettings {
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

// issue is the first problem with a configured sub-agent, or nil. Nothing is
// checked while no URL is set.
func (settings SubagentSettings) issue() *ReadinessIssue {
	if strings.TrimSpace(settings.URL) == "" {
		return nil
	}
	normalized := settings.normalized()
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
