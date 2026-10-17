package app

import (
	"context"
	"errors"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// ListChatTasks lists a chat's scheduled and daily tasks for the GUI, soonest
// first.
func (application *Application) ListChatTasks(ctx context.Context, chatID string) ([]control.AgentChatTask, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return nil, err
	}
	tasks, err := runtime.inboundDispatch.Tasks(ctx, key)
	if err != nil {
		return nil, err
	}
	result := make([]control.AgentChatTask, len(tasks))
	for index, task := range tasks {
		result[index] = controlChatTask(task)
	}
	return result, nil
}

// AddChatTask schedules a task from the GUI, as the bot's /schedule-task or
// /daily-task would.
func (application *Application) AddChatTask(ctx context.Context, chatID string, input control.AgentChatTaskInput) (control.AgentChatTask, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return control.AgentChatTask{}, err
	}
	if !input.Daily && !input.RunAt.After(time.Now()) {
		return control.AgentChatTask{}, agent.NewError(agent.ErrorInvalidArgument, "add chat task", errors.New("the run time must be in the future"))
	}
	// Each task needs its own source; a GUI task has no command message.
	source, err := identity.NewMessageID()
	if err != nil {
		return control.AgentChatTask{}, agent.NewError(agent.ErrorInternal, "add chat task", err)
	}
	var task command.Task
	if input.Daily {
		task, err = runtime.inboundDispatch.ScheduleDailyTask(ctx, key, source, input.DailyMinute, input.Prompt)
	} else {
		task, err = runtime.inboundDispatch.ScheduleTask(ctx, key, source, input.RunAt, input.Prompt)
	}
	if err != nil {
		return control.AgentChatTask{}, err
	}
	return controlChatTask(task), nil
}

// DeleteChatTask deletes a chat's task by the ID the GUI listed.
func (application *Application) DeleteChatTask(ctx context.Context, chatID, taskID string, daily bool) error {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return err
	}
	deleted, err := runtime.inboundDispatch.CancelTask(ctx, key, taskID, daily)
	if err != nil {
		return err
	}
	if !deleted {
		return agent.NewError(agent.ErrorNotFound, "delete chat task", errors.New("the task no longer exists"))
	}
	return nil
}

func controlChatTask(task command.Task) control.AgentChatTask {
	return control.AgentChatTask{ID: task.Code, Prompt: task.Prompt, NextRun: task.FireAt, Daily: task.Daily}
}
