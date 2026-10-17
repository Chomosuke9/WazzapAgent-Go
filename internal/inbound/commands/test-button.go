package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "test-button",
		Permission:  "public and !fromMe",
		Description: "Sends test buttons; tapping one replies with which button was tapped.",
		Run:         runTestButton,
	})
}

// testButtonChoices maps each button's Args to the label the user sees.
var testButtonChoices = map[string]string{"a": "Button A", "b": "Button B", "c": "Button C"}

func runTestButton(ctx context.Context, c *command.Context) error {
	if !c.HasArgs {
		return c.ReplyButtons(ctx, "Tap a button to test button handling.",
			command.Button{Label: testButtonChoices["a"], Args: "a"},
			command.Button{Label: testButtonChoices["b"], Args: "b"},
			command.Button{Label: testButtonChoices["c"], Args: "c"},
		)
	}
	// A tap arrives here as "/test-button <args>".
	label, ok := testButtonChoices[strings.TrimSpace(c.Args)]
	if !ok {
		return c.Reply(ctx, "Usage: /test-button, then tap one of the buttons.")
	}
	return c.Reply(ctx, "Button works. You tapped "+label+".")
}
