package app

import (
	"context"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/inbound"
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
	// The model sees times in the computer's time zone (TZ overrides it),
	// the one daily tasks follow and the prompt's date uses.
	chat.Location = time.Local
	for _, task := range tasks {
		if task.Key == key {
			chat.Tasks = append(chat.Tasks, agent.ScheduledTaskSummary{
				Code: task.Code(), FireAt: task.FireAt, Daily: task.Daily, Prompt: task.Prompt,
			})
		}
	}
	return chat, nil
}
