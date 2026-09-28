package app

import (
	"context"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

type scheduledTaskLister interface {
	ListScheduledTasks(context.Context, identity.TenantID) ([]inbound.ScheduledTask, error)
}

// chatStateReader adds the chat's one-off and daily tasks to the WhatsApp
// chat metadata, so the model's <chat_state> lists them.
type chatStateReader struct {
	chats agent.ChatContextReader
	tasks scheduledTaskLister
}

func (reader chatStateReader) ReadChatContext(ctx context.Context, key agent.Key) (agent.ChatContext, error) {
	chat, err := reader.chats.ReadChatContext(ctx, key)
	if err != nil {
		return agent.ChatContext{}, err
	}
	tasks, err := reader.tasks.ListScheduledTasks(ctx, key.TenantID)
	if err != nil {
		return agent.ChatContext{}, err
	}
	// Daily tasks follow the computer's time zone, as the dispatcher does.
	chat.TimeZone = utcOffset(time.Now().In(time.Local))
	for _, task := range tasks {
		if task.Key == key {
			chat.Tasks = append(chat.Tasks, agent.ScheduledTaskSummary{
				Code: task.Code(), FireAt: task.FireAt.In(time.Local), Daily: task.Daily, Prompt: task.Prompt,
			})
		}
	}
	return chat, nil
}

// utcOffset writes the offset of t's zone as "UTC+07:00", or "UTC".
func utcOffset(t time.Time) string {
	if _, offset := t.Zone(); offset == 0 {
		return "UTC"
	}
	return "UTC" + t.Format("-07:00")
}
