package command

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// Context is everything a running command may use.
type Context struct {
	// Name is the canonical name of the running command.
	Name string
	// Args is the text after "/name ", verbatim. HasArgs is true when the
	// token was followed by a space, even if Args is empty.
	Args    string
	HasArgs bool
	Message conversation.IncomingMessage
	Facts   PermissionFacts
	Agent   *agent.Agent
	// Config is the chat's config as read just before the command ran.
	Config agent.ConfigSnapshot

	registry   *Registry
	invocation Invocation
}

// Key identifies the chat the command runs in.
func (c *Context) Key() agent.Key {
	return agent.Key{TenantID: c.Message.TenantID, AccountID: c.Message.AccountID, ChatID: c.Message.ChatID}
}

// Commands lists every registered command, sorted by name.
func (c *Context) Commands() []Command { return c.registry.Commands() }

// Reply sends text to the chat.
func (c *Context) Reply(ctx context.Context, text string) error {
	sender := c.invocation.Platform.Text
	if sender == nil {
		return c.unavailable("text replies")
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create /"+c.Name+" reply ID", err)
	}
	_, err = sender.SendText(ctx, action.SendTextRequest{Key: c.Key(), ActionID: actionID, Text: text})
	return err
}

// ReplyButtons sends text with quick-reply buttons. Every button belongs to
// this command: a tap runs "/<Name> <Args>" through Run.
func (c *Context) ReplyButtons(ctx context.Context, text string, buttons ...Button) error {
	if len(buttons) == 0 || len(buttons) > action.MaxButtons {
		return agent.NewError(agent.ErrorInvalidArgument, "send /"+c.Name+" buttons", fmt.Errorf("1 to %d buttons are required", action.MaxButtons))
	}
	sender := c.invocation.Platform.Buttons
	if sender == nil {
		// Hosts without buttons still get something usable: the commands
		// the buttons would have sent, as text the user can type.
		lines := []string{text, ""}
		for _, button := range buttons {
			lines = append(lines, "• "+button.Label+": "+c.buttonID(button))
		}
		return c.Reply(ctx, strings.Join(lines, "\n"))
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create /"+c.Name+" reply ID", err)
	}
	request := action.SendButtonsRequest{Key: c.Key(), ActionID: actionID, Text: text, Buttons: make([]action.Button, 0, len(buttons))}
	for _, button := range buttons {
		request.Buttons = append(request.Buttons, action.Button{ID: c.buttonID(button), Label: button.Label})
	}
	_, err = sender.SendButtons(ctx, request)
	return err
}

// buttonID is what a tap sends back: this command with the button's args.
func (c *Context) buttonID(button Button) string {
	if button.Args == "" {
		return "/" + c.Name
	}
	return "/" + c.Name + " " + button.Args
}

// Group returns the group moderation port.
func (c *Context) Group() (GroupModerator, error) {
	if c.invocation.Platform.Group == nil {
		return nil, c.unavailable("group moderation")
	}
	return c.invocation.Platform.Group, nil
}

// QuotedRaw returns the raw provider payload of the message this command
// replied to. It is captured only for /catch invocations.
func (c *Context) QuotedRaw(ctx context.Context) (RawQuotedMessage, error) {
	if c.invocation.Store == nil {
		return RawQuotedMessage{}, agent.NewError(agent.ErrorNotFound, "read quoted message", errors.New("command has no durable inbox record"))
	}
	return c.invocation.Store.ReadRawQuotedMessage(ctx, c.Message)
}

// ResetHistory clears the chat's conversation history.
func (c *Context) ResetHistory(ctx context.Context) error {
	if err := c.Agent.History().Reset(ctx, c.Config.Version); err != nil {
		return err
	}
	if c.invocation.Observer != nil {
		c.invocation.Observer.ObserveHistoryReset()
	}
	return nil
}

// UpdateConfig applies change to the chat's config and returns the new
// snapshot. change must set absolute values: a command replayed after a crash
// runs change again, and a write that already landed then changes nothing, so
// it is skipped instead of being applied twice.
func (c *Context) UpdateConfig(ctx context.Context, change func(*agent.ConfigValues)) (agent.ConfigSnapshot, error) {
	desired := c.Config.Values()
	change(&desired)
	if err := agent.ValidateConfigValues(desired); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if reflect.DeepEqual(desired, c.Config.Values()) {
		return c.Config, nil
	}
	return c.Agent.Config().Update(ctx, c.Config.Version, change)
}

func (c *Context) unavailable(capability string) error {
	return agent.NewError(agent.ErrorUnavailable, "run /"+c.Name, fmt.Errorf("%s: not available in this host", capability))
}
