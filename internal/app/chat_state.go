package app

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

type scheduledTaskLister interface {
	ListScheduledTasks(context.Context, identity.TenantID) ([]inbound.ScheduledTask, error)
}

// chatStateReader adds the chat's scheduled tasks to the WhatsApp chat
// metadata, so the model's <chat_state> lists them.
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
	for _, task := range tasks {
		if task.Key == key {
			chat.Tasks = append(chat.Tasks, agent.ScheduledTaskSummary{FireAt: task.FireAt, Prompt: task.Prompt})
		}
	}
	return chat, nil
}
