package app

import (
	"context"
	"errors"
	"time"

	whatsappadapter "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/WazzapAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

func NormalizeWhatsAppBroadcastPayload(payload string) (string, error) {
	return whatsappadapter.NormalizeBroadcastPayload(payload)
}

func (application *Application) ListBroadcastGroups(ctx context.Context) ([]control.AgentBroadcastGroup, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "list WhatsApp broadcast groups", errors.New("WhatsApp Agent is not running"))
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

func (application *Application) BroadcastWhatsAppGroups(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int) ([]control.AgentBroadcastGroupResult, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "send WhatsApp broadcast", errors.New("WhatsApp Agent is not running"))
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

func (application *Application) ScheduleWhatsAppBroadcast(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int, scheduledAt time.Time) (control.AgentBroadcastSchedule, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return control.AgentBroadcastSchedule{}, agent.NewError(agent.ErrorNotReady, "schedule WhatsApp broadcast", errors.New("WhatsApp Agent is not running"))
	}
	schedule, err := adapter.ScheduleBroadcast(ctx, groupIDs, format, payload, batchSize, batchDelaySeconds, scheduledAt)
	if err != nil {
		return control.AgentBroadcastSchedule{}, err
	}
	return agentBroadcastSchedule(schedule), nil
}

func (application *Application) ListWhatsAppBroadcastSchedules(ctx context.Context) ([]control.AgentBroadcastSchedule, error) {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "list WhatsApp broadcast schedules", errors.New("WhatsApp Agent is not running"))
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

func (application *Application) CancelWhatsAppBroadcastSchedule(ctx context.Context, id string) error {
	adapter := application.adapterState.Load()
	if adapter == nil {
		return agent.NewError(agent.ErrorNotReady, "cancel WhatsApp broadcast schedule", errors.New("WhatsApp Agent is not running"))
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
