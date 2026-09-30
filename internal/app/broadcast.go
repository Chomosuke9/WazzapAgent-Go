package app

import (
	"context"
	"errors"
	"time"

	discordadapter "github.com/Chomosuke9/DiscordAgent-Go/internal/adapters/discord"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/DiscordAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
)

func NormalizeDiscordBroadcastPayload(payload string) (string, error) {
	return discordadapter.NormalizeBroadcastPayload(payload)
}

func (application *Application) ListBroadcastGroups(ctx context.Context) ([]control.AgentBroadcastGroup, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "list Discord broadcast groups", errors.New("Discord Agent is not running"))
	}
	groups, err := adapter.ListBroadcastGroups(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]control.AgentBroadcastGroup, len(groups))
	for index, group := range groups {
		result[index] = control.AgentBroadcastGroup{ID: group.ID, Name: group.Name}
	}
	return result, nil
}

func (application *Application) BroadcastDiscordGroups(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int) ([]control.AgentBroadcastGroupResult, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "send Discord broadcast", errors.New("Discord Agent is not running"))
	}
	results, err := adapter.BroadcastGroups(ctx, groupIDs, format, payload, batchSize, batchDelaySeconds)
	if err != nil {
		return nil, err
	}
	converted := make([]control.AgentBroadcastGroupResult, len(results))
	for index, result := range results {
		converted[index] = control.AgentBroadcastGroupResult{
			ID: result.ID, Name: result.Name, Sent: result.Sent, ErrorCode: string(result.ErrorCode),
		}
	}
	return converted, nil
}

func (application *Application) ScheduleDiscordBroadcast(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int, scheduledAt time.Time) (control.AgentBroadcastSchedule, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return control.AgentBroadcastSchedule{}, agent.NewError(agent.ErrorNotReady, "schedule Discord broadcast", errors.New("Discord Agent is not running"))
	}
	schedule, err := adapter.ScheduleBroadcast(ctx, groupIDs, format, payload, batchSize, batchDelaySeconds, scheduledAt)
	if err != nil {
		return control.AgentBroadcastSchedule{}, err
	}
	return agentBroadcastSchedule(schedule), nil
}

func (application *Application) ListDiscordBroadcastSchedules(ctx context.Context) ([]control.AgentBroadcastSchedule, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "list Discord broadcast schedules", errors.New("Discord Agent is not running"))
	}
	schedules, err := adapter.ListBroadcastSchedules(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]control.AgentBroadcastSchedule, len(schedules))
	for index, schedule := range schedules {
		result[index] = agentBroadcastSchedule(schedule)
	}
	return result, nil
}

func (application *Application) CancelDiscordBroadcastSchedule(ctx context.Context, id string) error {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return agent.NewError(agent.ErrorNotReady, "cancel Discord broadcast schedule", errors.New("Discord Agent is not running"))
	}
	return adapter.CancelBroadcastSchedule(ctx, id)
}

func agentBroadcastSchedule(schedule broadcastmodel.Schedule) control.AgentBroadcastSchedule {
	result := control.AgentBroadcastSchedule{
		ID: schedule.ID, ScheduledAt: schedule.ScheduledAt, BatchSize: schedule.BatchSize,
		BatchDelaySeconds: schedule.BatchDelaySeconds, GroupCount: len(schedule.Targets), Status: string(schedule.Status),
		Results: make([]control.AgentBroadcastScheduleResult, len(schedule.Results)),
	}
	for index, item := range schedule.Results {
		result.Results[index] = control.AgentBroadcastScheduleResult{Name: item.Name, Sent: item.Sent, ErrorCode: item.ErrorCode}
	}
	return result
}
