# Slash commands

This folder holds every slash command: **one command per file**. Each file
owns everything about its command, including the name, aliases, permission,
argument parsing, replies, config changes, and buttons. Commands never import
each other, and adding a command never requires editing a file outside this
folder.

If you are an agent adding a command, read "How a command runs" once, then
follow "Adding a command".

## How a command runs

```text
WhatsApp message "/trigger mention off"        (or a tap on a button with that ID)
  │
  ├─ adapter normalizes it to text             adapters/whatsapp/hypermeow/normalize.go
  │    a button tap's ID becomes the text, so taps and typing are identical
  ├─ inbound sees a registered "/token"        inbound/split.go → command lane
  ├─ permission facts are resolved             owner / admin / group / private / fromMe
  ├─ Registry.Dispatch                         internal/command/registry.go
  │    1. evaluates the command's Permission
  │         denied → DeniedReply is sent, Run is not called
  │    2. calls Run(ctx, c)
  │    3. Run returned nil → message marked handled
  │       Run returned an error → message stays unhandled and is retried later
  └─ your Run function                         this folder
```

Registration happens in `init()`: each file calls `register(...)` (defined in
`commands.go`). At startup, `inbound` builds the registry from `All()`. If two
commands claim the same name or alias, or a permission expression is invalid,
the app refuses to start. There is no generator and no switch statement.

## Adding a command

1. Create `internal/inbound/commands/<name>.go`, following the template below.
2. Add `<name>_test.go` next to it (see "Testing a command").
3. Run:
   ```sh
   gofmt -l .                       # must print nothing
   go vet ./...
   go test ./internal/inbound/...
   ```

That's all. Don't edit `commands.go`, `internal/command`, `internal/inbound`,
the SQLite store, or any other command's file.

### Template

```go
package commands

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "ping",                 // lowercase, [a-z][a-z0-9-]*, no slash
		Aliases:     []string{"p"},          // optional
		Permission:  "public",               // required, see "Permissions"
		Description: "Replies with pong.",   // shown by /help and to the model
		DeniedReply: "",                     // optional; sent when Permission denies
		Run:         runPing,
	})
}

func runPing(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "The /ping command does not accept arguments.")
	}
	return c.Reply(ctx, "pong")
}
```

Name the run function `run<Name>` and keep helper names specific to the
command (`parsePingArgs`, not `parseArgs`). Every file is in the same Go
package, so a generic helper name will collide with another command.

## What `c *command.Context` gives you

| Member | Use |
|---|---|
| `c.Args` | Text after `/name `, verbatim (not trimmed). |
| `c.HasArgs` | `true` when a space followed the name, even if `Args` is empty. `/ping` → false, `/ping ` → true. |
| `c.Name` | Canonical name of the running command. The same when invoked through an alias. |
| `c.Message` | The normalized message: `Text`, `Quote` (the message being replied to), `ChatKind`, `FromMe`, and so on. |
| `c.Facts` | Permission facts: `IsOwner`, `IsAdmin`, `BotIsAdmin`, `IsGroup`, `IsPrivate`, `FromMe`. |
| `c.Config` | The chat's config snapshot, read just before `Run`: model, prompt override, permission level, triggers. |
| `c.Agent` | The chat's Agent. Use `c.Agent.History()` or `c.Agent.BuildInput(...)` for read-only access. |
| `c.Key()` | The chat key (tenant, account, chat). |
| `c.Reply(ctx, text)` | Sends text to the chat. |
| `c.ReplyButtons(ctx, text, buttons...)` | Sends text with buttons owned by this command (see "Buttons"). |
| `c.UpdateConfig(ctx, func(*agent.ConfigValues))` | Changes the chat config. Crash-safe (see "Changing config"). |
| `c.ResetHistory(ctx)` | Clears the chat history. |
| `c.Group()` | The group moderation port: announce, description, revoke, kick, mute. Returns an error if unavailable. |
| `c.QuotedRaw(ctx)` | The raw provider JSON of the quoted message. It is only captured for `/catch`. |
| `c.Commands()` | Every registered command, sorted. Used by `/help`. |

If a command needs something that isn't in this table, that is a system
change, not a command change. See "Needing a new capability".

## Permissions

`Permission` is a boolean expression evaluated on every invocation and every
button tap:

- Atoms: `public`, `owner`/`isOwner`, `admin`/`isAdmin`/`senderIsAdmin`,
  `group`/`isGroup`, `private`/`isPrivate`, `fromMe`/`from_me`.
- Operators, highest precedence first: `!`, `and`, `or`. Use parentheses.
- Commands the AI model issues inside its reply run with `fromMe=true`. Add
  `and !fromMe` when the bot must not run the command itself.

```go
Permission: "public"                                      // anyone, including the bot
Permission: "owner and !fromMe"                           // configured owner only
Permission: "(owner or isAdmin) and isGroup and !fromMe"  // owner or admin, in groups
```

Never check permissions inside `Run`. The registry has already done it.

## Replies and errors

- **Bad input is not an error.** Reply with the usage text and return
  `nil`, so the message is marked handled.
- **Return an error only for real failures** (storage, provider, network).
  The message then stays unhandled and is retried, so don't return an error
  for something retrying cannot fix.
- If `Run` returns `nil` without replying, that's fine. `/group delete`
  does this on purpose.
- Use `agent.NewError(agent.ErrorXxx, "operation", err)` for errors, like
  the rest of the codebase.

## Buttons

Buttons belong to the command that sends them. A button's ID is always
`/<this command> <Args>`, so a tap comes back through the normal router into
**this file's `Run`**, exactly as if the user had typed it, with the same
permission check. You never write a separate button handler.

```go
return c.ReplyButtons(ctx, "Mention trigger is on.",
	command.Button{Label: "Turn off", Args: "mention off"},  // ID "/trigger mention off"
	command.Button{Label: "Show all", Args: ""},             // ID "/trigger"
)
```

- A message can have 1 to 10 buttons. Button labels must not be empty.
- `Args` must be valid input for your own `Run`. Add a test that parses every
  button you send (see `TestTriggerViewOffersToggleButtonsThatRouteBackToTrigger`).
- When the host can't send buttons, `ReplyButtons` falls back to a text reply
  that lists each label next to the command to type.
- Taps on list, legacy buttons, template and native-flow messages are all
  handled by the adapter. The ID is read before the message text, and
  numeric IDs are accepted. You never deal with provider types.

## Changing config

```go
updated, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
	values.Permission.ModerationLevel = level
})
```

- Set **absolute** values (`= level`), never relative ones (`++`, toggles
  based on the current value). After a crash, the command is replayed. The
  journal treats the write as already done when running your function again
  would change nothing, and that check only works for absolute values.
  For a toggle, compute the target value when you parse the arguments, as
  `trigger.go` does with `on`/`off`.
- Validate first and reply with usage on bad input. `UpdateConfig` returns an
  `ErrorInvalidArgument` if the result fails config validation.
- Never call `c.Agent.Config().Set*` or `Update` directly. That bypasses
  the crash-safe journal.

## Testing a command

Test through the real registry, the way production runs the command. The
shared helpers are in `helpers_test.go`:

- `builtinRegistry(t)`: a registry built from every command file.
- `recordingText`: a fake that records `SendText` calls.
- `recordingButtons`: a fake that records `SendButtons` calls.
- `handledStore`: counts how many times a message was marked handled.
- `testChatKey(t)`: a valid chat key.

```go
func TestPingRepliesPong(t *testing.T) {
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse("/ping")
	if !recognized {
		t.Fatal("/ping is not registered")
	}
	text, store := &recordingText{}, &handledStore{}
	key := testChatKey(t)
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message:  conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, Text: "/ping"},
		Facts:    command.PermissionFacts{IsPrivate: true},
		Platform: command.Platform{Text: text},
		Store:    store,
	})
	if err != nil || len(text.sent) != 1 || text.sent[0] != "pong" || store.handled != 1 {
		t.Fatalf("err=%v sent=%q handled=%d", err, text.sent, store.handled)
	}
}
```

- Set `Facts` to the sender you are testing, and add a case where
  `Permission` denies the command. `Dispatch` then returns an error for which
  `errors.Is(err, command.ErrDenied)` is true, and `Run` is never called.
- If `Run` reads `c.Config`, set `Invocation.Config`. If it uses `c.Agent`,
  cover it with an end-to-end test in `internal/inbound/inbound_test.go`,
  which runs real SQLite and a real Agent.
- Keep argument parsing in a pure function (`parse<Name>Args`) and test it
  with a table test.

## Needing a new capability

A command can only use what `command.Context` exposes. If a command needs a
new provider ability (for example sending an image), it is a system change,
not a command change:

1. Add a port interface and a field to `command.Platform` in
   `internal/command/command.go`, plus a `Context` method that returns an
   `ErrorUnavailable` error when the port is nil.
2. Implement it in `internal/adapters/whatsapp/hypermeow`.
3. Wire it in `internal/app/compose.go` (`commandPlatform := command.Platform{...}`).

Don't type-assert `Platform` fields, and don't add methods named after one
command to shared interfaces. That coupling is exactly what this layout exists
to prevent.

## Rules

- One command per file. The file name matches the command name.
- No imports between command files, and no shared mutable state between
  commands.
- Don't edit another command's file to add yours.
- Don't check permissions inside `Run`. Don't mark messages handled yourself.
- Don't import provider packages (`hypermeow`, `waE2E`, and so on) in this
  folder.
- Keep replies in English, matching the existing commands.
