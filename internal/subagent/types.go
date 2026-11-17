// Package subagent connects the bot to a WazzapSubAgents service: an external
// worker with a shell, files and internet access. The model delegates a task
// with execute_subtask; the service works on it for seconds to hours, reports
// progress to the webhook, and calls back with a report and output files. The
// model is then run again to tell the chat the result, and the files follow.
//
// Calling execute_subtask while a task is running steers that task instead of
// starting a second one. Calling it on the turn that delivers a result starts
// a correction task that keeps the finished task's memory.
package subagent

import (
	"context"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	// MaxFileBytes is the largest input or output file handled. A larger
	// output is skipped and named in the result the model reads.
	MaxFileBytes = 200 * 1024 * 1024
	// maxOutputFiles and maxInlineOutputBytes bound one completion callback.
	maxOutputFiles       = 64
	maxInlineOutputBytes = 256 * 1024 * 1024
	// maxProgressEntries is how many progress entries a running task keeps.
	maxProgressEntries = 100
	// recentFinishedAge is how long a finished, not yet delivered task is
	// shown to the model as "recently finished".
	recentFinishedAge = 5 * time.Minute
)

// Config is the sub-agent connection. URL empty means there is no sub-agent.
type Config struct {
	// URL is the WazzapSubAgents base URL, such as http://localhost:5000.
	URL string
	// APIToken authenticates this app to the service (Authorization: Bearer).
	APIToken string
	// WebhookURL is where the service calls back.
	WebhookURL string
	// WebhookToken, when set, must come with every callback, as
	// X-Subagent-Webhook-Token or Authorization: Bearer.
	WebhookToken string
	// DataDir holds staged input files and received output files.
	DataDir string
	// EnabledDefault applies to chats with no /subagent setting of their own
	// when no account default was set with /subagent default.
	EnabledDefault bool

	// WaitTimeout is how long a task may stay silent (no progress, queue or
	// completion callback) before it is given up. MaxWait bounds a task's
	// whole run whatever its progress.
	WaitTimeout time.Duration
	MaxWait     time.Duration

	HTTPTimeout           time.Duration
	SteerConsumeTimeout   time.Duration
	OutputDownloadTimeout time.Duration
	SubmitRetryMax        int
	RetryBaseBackoff      time.Duration
	RetryMaxBackoff       time.Duration
	// Files up to MaxInlineFileBytes, and MaxInlineTotalBytes in all, travel
	// inside the request; the rest are uploaded in chunks first.
	MaxInlineFileBytes  int64
	MaxInlineTotalBytes int64
	// ReportMaxChars and ProgressDetailMaxChars bound what the model reads.
	ReportMaxChars         int
	ProgressDetailMaxChars int
	WebhookMaxBodyBytes    int64
}

func (config Config) withDefaults() Config {
	duration := func(value *time.Duration, fallback time.Duration) {
		if *value <= 0 {
			*value = fallback
		}
	}
	duration(&config.WaitTimeout, 15*time.Minute)
	duration(&config.MaxWait, 2*time.Hour)
	duration(&config.HTTPTimeout, 2*time.Minute)
	duration(&config.SteerConsumeTimeout, 30*time.Minute)
	duration(&config.OutputDownloadTimeout, 15*time.Minute)
	duration(&config.RetryBaseBackoff, 2*time.Second)
	duration(&config.RetryMaxBackoff, time.Minute)
	if config.SubmitRetryMax <= 0 {
		config.SubmitRetryMax = 5
	}
	if config.MaxInlineFileBytes <= 0 {
		config.MaxInlineFileBytes = 50 * 1024 * 1024
	}
	if config.MaxInlineTotalBytes <= 0 {
		config.MaxInlineTotalBytes = 50 * 1024 * 1024
	}
	if config.ReportMaxChars <= 0 {
		config.ReportMaxChars = 4096
	}
	if config.ProgressDetailMaxChars <= 0 {
		config.ProgressDetailMaxChars = 500
	}
	if config.WebhookMaxBodyBytes <= 0 {
		config.WebhookMaxBodyBytes = 200 * 1024 * 1024
	}
	return config
}

type Status uint8

// Status values are stored in subagent_tasks.status.
const (
	StatusRunning   Status = 1
	StatusCompleted Status = 2
	StatusFailed    Status = 3
)

// Task is one execute_subtask run in a chat.
type Task struct {
	Key agent.Key
	// SessionID names the task to the service: "<chat ID>_<8 hex>_<unix time>".
	SessionID   string
	Instruction string
	HighQuality bool
	// PreviousSessionID is set for a correction task: the finished task
	// whose conversation the service carries over.
	PreviousSessionID string
	FileMessageIDs    []identity.MessageID
	// InvocationID and CausationID are the turn that delivers the result.
	// They are fixed when the task is created, so a delivery retried after a
	// crash resumes the same turn instead of answering twice.
	InvocationID identity.InvocationID
	CausationID  identity.CausationID
	Status       Status
	// Submitted is set once the service accepted the task.
	Submitted bool
	Result    Result
	// Report is the service's report, or the reason the task failed.
	Report      string
	StartedAt   time.Time
	EndedAt     time.Time
	DeliveredAt time.Time
}

// Result is a finished task's outcome as kept for delivery.
type Result struct {
	Success bool         `json:"success"`
	Files   []OutputFile `json:"files,omitempty"`
	// ContentDropped is set when the service could not transfer some output.
	ContentDropped bool `json:"content_dropped,omitempty"`
}

// OutputFile is one file a task produced, already received and verified, or
// the reason it was skipped.
type OutputFile struct {
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	Mime       string `json:"mime,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// ProgressEntry is one step a running task reported.
type ProgressEntry struct {
	Step   string
	Detail string
	// Reason says why the step ran. Older services send only Detail.
	Reason string
	At     time.Time
}

// SentFile is a file the bot sent to a chat, kept so a later task can take
// it as input.
type SentFile struct {
	Path string
	Name string
	Mime string
	Kind string
	Size int64
}

// Scope is what a /subagent setting applies to.
type Scope uint8

const (
	// ScopeChat sets one chat.
	ScopeChat Scope = iota + 1
	// ScopeDefault sets chats that have no setting of their own.
	ScopeDefault
	// ScopeGlobal sets every chat: the default, with all chat settings removed.
	ScopeGlobal
)

// Store keeps tasks, their delivery progress, sent files and the per-chat
// switch.
type Store interface {
	// CreateTask saves a running task. It fails with agent.ErrorConflict when
	// the chat already has a running task.
	CreateTask(context.Context, Task) error
	MarkTaskSubmitted(ctx context.Context, tenantID identity.TenantID, sessionID string) error
	// FinishTask ends a running task and reports whether it was running.
	FinishTask(ctx context.Context, tenantID identity.TenantID, sessionID string, status Status, report string, result Result, endedAt time.Time) (bool, error)
	// LoadTask fails with agent.ErrorNotFound for an unknown session.
	LoadTask(ctx context.Context, tenantID identity.TenantID, sessionID string) (Task, error)
	// ActiveTask returns the chat's running task, or nil.
	ActiveTask(context.Context, agent.Key) (*Task, error)
	// TaskByInvocation returns the task whose result turn is invocationID, or nil.
	TaskByInvocation(context.Context, agent.Key, identity.InvocationID) (*Task, error)
	// LatestUndelivered returns the chat's newest finished task that was not
	// delivered yet, or nil.
	LatestUndelivered(context.Context, agent.Key) (*Task, error)
	// ListUndelivered returns every running or undelivered task, oldest first.
	ListUndelivered(context.Context, identity.TenantID) ([]Task, error)
	DeliveryPartDone(ctx context.Context, tenantID identity.TenantID, sessionID, part string) (bool, error)
	MarkDeliveryPartDone(ctx context.Context, tenantID identity.TenantID, sessionID, part string) error
	MarkTaskDelivered(ctx context.Context, tenantID identity.TenantID, sessionID string, at time.Time) error
	ChatExists(context.Context, agent.Key) (bool, error)

	// SubagentEnabled returns the chat's setting, or else the account
	// default; set is false when neither exists.
	SubagentEnabled(context.Context, agent.Key) (enabled, set bool, err error)
	SetSubagentEnabled(ctx context.Context, key agent.Key, scope Scope, enabled bool) error
}

// Messenger sends to a chat and records what it sent in the chat's history.
type Messenger interface {
	// SendText sends text, quoting quoted when it is set.
	SendText(ctx context.Context, key agent.Key, text string, quoted identity.MessageID) error
	// SendFile sends one output file as its own message.
	SendFile(ctx context.Context, key agent.Key, file StagedFile) error
}

// MessageInput is what a chat message gives a task: its files, written
// under the directory asked for, and the text its sender typed.
type MessageInput struct {
	Paths []string
	Text  string
}

// Inputs reads a chat message for a task.
type Inputs interface {
	// MessageInput returns the message's file (downloading it into dir) and
	// its text. Both are empty when the message has neither.
	MessageInput(ctx context.Context, key agent.Key, messageID identity.MessageID, dir string) (MessageInput, error)
}

// Turns runs the model for a finished task.
type Turns interface {
	// SubagentTurn runs one turn whose input is text, a system entry at the
	// end of the chat's transcript; resultBlock is shown to the model for
	// this turn only, together with images, the task's image files. The
	// turn may call execute_subtask.
	SubagentTurn(ctx context.Context, key agent.Key, invocationID identity.InvocationID, causationID identity.CausationID, text, resultBlock string, images []agent.ModelImage, requestedAt time.Time) (agent.InvokeResult, error)
}
