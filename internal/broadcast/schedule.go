package broadcast

import (
	"context"
	"time"
)

type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusSending   Status = "sending"
	StatusCompleted Status = "completed"
	StatusPartial   Status = "partial"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Target contains a provider address that stays inside the account runtime and
// its tenant-scoped store. It is never included in a UI schedule response.
type Target struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

type Result struct {
	Name      string `json:"name"`
	Sent      bool   `json:"sent"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type Schedule struct {
	ID                string
	ScheduledAt       time.Time
	Format            string
	Payload           string
	BatchSize         int
	BatchDelaySeconds int
	Targets           []Target
	Status            Status
	Results           []Result
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Store interface {
	SaveBroadcastSchedule(context.Context, Schedule) error
	ListBroadcastSchedules(context.Context) ([]Schedule, error)
	ClaimDueBroadcastSchedule(context.Context, time.Time) (*Schedule, error)
	CompleteBroadcastSchedule(context.Context, string, Status, []Result) error
	CancelBroadcastSchedule(context.Context, string) error
	RecoverInterruptedBroadcastSchedules(context.Context) error
}
