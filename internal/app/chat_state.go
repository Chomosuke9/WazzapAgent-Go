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

type memoryLister interface {
	Memories(context.Context, agent.Key) ([]agent.Memory, error)
}

type stickerLister interface {
	StickerNames(context.Context, agent.Key) ([]string, error)
}

// chatStateReader adds the chat's one-off and daily tasks, long-term memory
// and sticker names to the WhatsApp chat metadata, so the model's
// <chat_state> lists them.
type chatStateReader struct {
	chats    agent.ChatContextReader
	tasks    scheduledTaskLister
	memories memoryLister
	stickers stickerLister
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
	if chat.Memories, err = reader.memories.Memories(ctx, key); err != nil {
		return agent.ChatContext{}, err
	}
	if chat.Stickers, err = reader.stickers.StickerNames(ctx, key); err != nil {
		return agent.ChatContext{}, err
	}
	return chat, nil
}
