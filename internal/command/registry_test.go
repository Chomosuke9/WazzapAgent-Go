package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

func noop(context.Context, *command.Context) error { return nil }

func TestRegistryCanonicalizesAliasesAndKeepsMalformedArgumentsRecognized(t *testing.T) {
	registry, err := command.NewRegistry([]command.Command{
		{Name: "prompt", Aliases: []string{"prompts"}, Permission: "owner", Run: noop},
		{Name: "help", Aliases: []string{"menu"}, Permission: "public", Run: noop},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request, cmd, recognized := registry.Parse("/PROMPTS set x")
	if !recognized || request.Name != "prompt" || request.Arguments != "set x" || !request.ArgumentsPresent || cmd.Name != "prompt" {
		t.Fatalf("parsed command = %#v, %#v, %v", request, cmd, recognized)
	}
	request, _, recognized = registry.Parse("/prompt ")
	if !recognized || !request.ArgumentsPresent || request.Arguments != "" {
		t.Fatalf("trailing command separator was lost: %#v, %v", request, recognized)
	}
	if _, _, recognized := registry.Parse("/prompt unknown syntax"); !recognized {
		t.Fatal("known command with invalid arguments fell through")
	}
	if _, _, recognized := registry.Parse("/unregistered"); recognized {
		t.Fatal("unknown command was recognized")
	}
}

func TestRegistryRejectsAliasCollision(t *testing.T) {
	_, err := command.NewRegistry([]command.Command{
		{Name: "help", Aliases: []string{"menu"}, Permission: "public", Run: noop},
		{Name: "info", Aliases: []string{"menu"}, Permission: "public", Run: noop},
	})
	if err == nil {
		t.Fatal("alias collision was accepted")
	}
}

func TestRegistryRejectsCommandWithoutRun(t *testing.T) {
	_, err := command.NewRegistry([]command.Command{{Name: "help", Permission: "public"}})
	if !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("missing Run error = %v, want invalid_argument", err)
	}
}

func TestRegistryDispatchesCanonicalAndAliasRequests(t *testing.T) {
	called := ""
	registry, err := command.NewRegistry([]command.Command{{
		Name: "help", Aliases: []string{"menu"}, Permission: "public",
		Run: func(_ context.Context, c *command.Context) error {
			called = c.Name
			return nil
		},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "MENU"}, command.Invocation{}); err != nil {
		t.Fatalf("dispatch alias: %v", err)
	}
	if called != "help" {
		t.Fatalf("Run saw name %q, want help", called)
	}
}

func TestRegistryEnforcesPermissionForHumanAndBotOrigins(t *testing.T) {
	called := false
	registry, err := command.NewRegistry([]command.Command{{
		Name: "help", Permission: "public and !fromMe",
		Run: func(context.Context, *command.Context) error {
			called = true
			return nil
		},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request, _, _ := registry.Parse("/help")
	if err := registry.Dispatch(context.Background(), request, command.Invocation{Facts: command.PermissionFacts{FromMe: true}}); !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("bot dispatch error = %v, want permission_denied", err)
	}
	if called {
		t.Fatal("permission-denied command ran")
	}
	if err := registry.Dispatch(context.Background(), request, command.Invocation{}); err != nil {
		t.Fatalf("human dispatch: %v", err)
	}
	if !called {
		t.Fatal("allowed command did not run")
	}
}

func TestRegistryRejectsInvalidPermissionAtConstruction(t *testing.T) {
	_, err := command.NewRegistry([]command.Command{{Name: "help", Permission: "public and", Run: noop}})
	if !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("invalid permission error = %v, want invalid_argument", err)
	}
}

type handledStore struct {
	command.Store
	handled int
}

func (store *handledStore) MarkCommandHandled(context.Context, conversation.IncomingMessage) error {
	store.handled++
	return nil
}

func TestRegistryMarksHandledOnlyAfterRunSucceeds(t *testing.T) {
	fail := agent.NewError(agent.ErrorProviderFailure, "send", nil)
	registry, err := command.NewRegistry([]command.Command{
		{Name: "ok", Permission: "public", Run: noop},
		{Name: "fail", Permission: "public", Run: func(context.Context, *command.Context) error { return fail }},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	store := &handledStore{}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "fail"}, command.Invocation{Store: store}); err == nil {
		t.Fatal("failing command returned nil")
	}
	if store.handled != 0 {
		t.Fatal("failed command was marked handled")
	}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "ok"}, command.Invocation{Store: store}); err != nil {
		t.Fatalf("dispatch ok: %v", err)
	}
	if store.handled != 1 {
		t.Fatalf("handled marks = %d, want 1", store.handled)
	}
}

type buttonRecorder struct{ sent []action.SendButtonsRequest }

func (recorder *buttonRecorder) SendButtons(_ context.Context, request action.SendButtonsRequest) (action.SendTextResult, error) {
	recorder.sent = append(recorder.sent, request)
	return action.SendTextResult{}, nil
}

type textRecorder struct{ sent []string }

func (recorder *textRecorder) SendText(_ context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	recorder.sent = append(recorder.sent, request.Text)
	return action.SendTextResult{}, nil
}

func TestButtonsRouteBackToTheCommandThatSentThem(t *testing.T) {
	registry, err := command.NewRegistry([]command.Command{{
		Name: "vote", Permission: "public",
		Run: func(ctx context.Context, c *command.Context) error {
			return c.ReplyButtons(ctx, "Pick one", command.Button{Label: "Yes", Args: "yes"}, command.Button{Label: "Menu"})
		},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	buttons := &buttonRecorder{}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "vote"}, command.Invocation{Platform: command.Platform{Buttons: buttons}}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := buttons.sent[0].Buttons
	if len(got) != 2 || got[0].ID != "/vote yes" || got[0].Label != "Yes" || got[1].ID != "/vote" {
		t.Fatalf("button IDs = %#v", got)
	}
	if request, _, recognized := registry.Parse(got[0].ID); !recognized || request.Name != "vote" || request.Arguments != "yes" {
		t.Fatalf("tap %q does not route back to /vote: %#v", got[0].ID, request)
	}

	text := &textRecorder{}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "vote"}, command.Invocation{Platform: command.Platform{Text: text}}); err != nil {
		t.Fatalf("dispatch without button support: %v", err)
	}
	if len(text.sent) != 1 || text.sent[0] != "Pick one\n\n• Yes: /vote yes\n• Menu: /vote" {
		t.Fatalf("text fallback = %q", text.sent)
	}
}

type rejectingButtons struct {
	code  agent.ErrorCode
	calls int
}

func (sender *rejectingButtons) SendButtons(context.Context, action.SendButtonsRequest) (action.SendTextResult, error) {
	sender.calls++
	return action.SendTextResult{}, agent.NewError(sender.code, "send WhatsApp buttons", errors.New("server returned error 405"))
}

func TestRejectedButtonsFallBackToText(t *testing.T) {
	registry, err := command.NewRegistry([]command.Command{{
		Name: "vote", Permission: "public",
		Run: func(ctx context.Context, c *command.Context) error {
			return c.ReplyButtons(ctx, "Pick one", command.Button{Label: "Yes", Args: "yes"})
		},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	buttons, text := &rejectingButtons{code: agent.ErrorUnsupported}, &textRecorder{}
	err = registry.Dispatch(context.Background(), command.Request{Name: "vote"}, command.Invocation{Platform: command.Platform{Text: text, Buttons: buttons}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if buttons.calls != 1 || len(text.sent) != 1 || text.sent[0] != "Pick one\n\n• Yes: /vote yes" {
		t.Fatalf("button calls=%d text=%q", buttons.calls, text.sent)
	}

	// An ambiguous failure may have delivered the buttons, so no text copy.
	buttons, text = &rejectingButtons{code: agent.ErrorProviderFailure}, &textRecorder{}
	err = registry.Dispatch(context.Background(), command.Request{Name: "vote"}, command.Invocation{Platform: command.Platform{Text: text, Buttons: buttons}})
	if !agent.IsCode(err, agent.ErrorProviderFailure) || len(text.sent) != 0 {
		t.Fatalf("ambiguous failure: err=%v text=%q", err, text.sent)
	}
}

func TestMenusRouteOptionsToTheirCommandsAndFallBackToText(t *testing.T) {
	registry, err := command.NewRegistry([]command.Command{{
		Name: "settings", Permission: "public",
		Run: func(ctx context.Context, c *command.Context) error {
			return c.ReplyMenus(ctx, "Current: level 1", "Tap a menu", command.Menu{Title: "Moderation", Options: []command.Button{
				{Label: "Level 0", Description: "Off", Command: "permission", Args: "0"},
				{Label: "Show all", Command: "permission"},
			}})
		},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	buttons := &buttonRecorder{}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "settings"}, command.Invocation{Platform: command.Platform{Buttons: buttons}}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	sent := buttons.sent[0]
	if sent.Footer != "Tap a menu" || len(sent.Buttons) != 0 || len(sent.Menus) != 1 || sent.Menus[0].Title != "Moderation" {
		t.Fatalf("menu request = %#v", sent)
	}
	if rows := sent.Menus[0].Rows; len(rows) != 2 || rows[0] != (action.MenuRow{ID: "/permission 0", Title: "Level 0", Description: "Off"}) || rows[1].ID != "/permission" {
		t.Fatalf("menu rows = %#v", rows)
	}

	text := &textRecorder{}
	err = registry.Dispatch(context.Background(), command.Request{Name: "settings"}, command.Invocation{Platform: command.Platform{Text: text, Buttons: &rejectingButtons{code: agent.ErrorUnsupported}}})
	if err != nil || len(text.sent) != 1 || text.sent[0] != "Current: level 1\n\n*Moderation*\n• Level 0: /permission 0\n• Show all: /permission" {
		t.Fatalf("text fallback = %q, %v", text.sent, err)
	}
}
