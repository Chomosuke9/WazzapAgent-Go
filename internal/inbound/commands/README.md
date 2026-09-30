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
Discord message "/trigger mention off"         (or a tap on a button with that ID)
  │
  ├─ adapter normalizes it to text             adapters/discord/normalize.go
  │    a button tap's ID becomes the text, so taps and typing are identical
  ├─ inbound sees a registered "/token"        inbound/split.go → command lane
  ├─ permission facts are resolved             owner / admin / group / private / fromMe
  ├─ Registry.Dispatch                         internal/command/registry.go
  │    1. evaluates the command's Permission
  │         denied → DeniedReply is sent, Run is not called
  │    2. calls Run(ctx, c)
  │    3. Run returned nil → message marked handled
  │       Run returned an error → error logged, message closed (never
  │       retried), and a short "failed" reply unless a send may have landed
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

	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "ping",                 // lowercase, [a-z][a-z0-9-]*, no slash
		Aliases:     []string{"p"},          // optional
		Permission:  "public",               // required, see "Permissions"
		Description: "Replies with pong.",   // shown by /help
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
| `c.ReplyButtons(ctx, text, buttons...)` | Sends text with buttons (see "Buttons"). |
| `c.ReplyMenus(ctx, text, footer, menus...)` | Sends text and a footer with list menus (see "Buttons"). |
| `c.UpdateConfig(ctx, func(*agent.ConfigValues))` | Changes the chat config. Crash-safe (see "Changing config"). |
| `c.ResetHistory(ctx)` | Clears the chat history. |
| `c.Group()` | The moderation port: lock/unlock (announce), topic (description), delete (revoke), kick, mute. Returns an error if unavailable. |
| `c.ScheduleTask(ctx, fireAt, prompt)` | Runs `prompt` as an AI turn in this chat at `fireAt` (at most a day ahead). Saved, so it survives a restart. |
| `c.QuotedRaw(ctx)` | The raw provider JSON of the quoted message. It is only captured for `/catch`. |
| `c.Commands()` | Every registered command, sorted. Used by `/help`. |

If a command needs something that isn't in this table, that is a system
change, not a command change. See "Needing a new capability".

## Permissions

`Permission` is a boolean expression evaluated on every invocation and every
button tap:

- Atoms: `public`, `owner`/`isOwner`, `admin`/`isAdmin`/`senderIsAdmin`,
  `group`/`isGroup` (a server channel or thread), `private`/`isPrivate` (a direct message), `fromMe`/`from_me`.
- Operators, highest precedence first: `!`, `and`, `or`. Use parentheses.
- Commands the AI model issues inside its reply run with `fromMe=true`, and
  `admin` means the bot may manage messages in the channel. Add `and !fromMe` when the bot must
  not run the command itself.
- The model's tool schema lists only the names of the commands it may run.
  Explain each of them, with its syntax and who may ask for it, in
  `internal/app/systemprompt.txt`; the model never sees `Description`.

```go
Permission: "public"                                      // anyone, including the bot
Permission: "owner and !fromMe"                           // configured owner only
Permission: "(owner or isAdmin) and isGroup and !fromMe"  // owner or moderator, in server channels
Permission: "fromMe"                                      // only the bot; people ask it
```

Never check permissions inside `Run`. The registry has already done it.
A chat setting that limits what a command does is not a permission check:
`/mod` holds the bot to the chat's moderation level (delete 1, mute 2,
kick 3).

## Replies and errors

- **Bad input is not an error.** Reply with the usage text and return
  `nil`, so the message is marked handled.
- **Return an error only for real failures** (storage, provider, network).
  The command lane logs the error and closes the message. It also replies
  "Sorry, /<name> failed. Please try again later." (or a "still starting up"
  message for `ErrorNotReady`), except after a timeout or provider failure,
  where your own reply may already have been delivered. A failed command is never re-run
  automatically, because re-running would repeat anything it already sent.
- If `Run` returns `nil` without replying, that's fine. `/mod delete`
  does this on purpose.
- Use `agent.NewError(agent.ErrorXxx, "operation", err)` for errors, like
  the rest of the codebase.

## Buttons

A button's ID is `/<command> <Args>`, where the command is the one that
sends it unless `Command` names another. A tap comes back through the normal
router into that command's `Run`, exactly as if the user had typed it, with
the same permission check. You never write a separate button handler.

```go
return c.ReplyButtons(ctx, "Mention trigger is on.",
	command.Button{Label: "Turn off", Args: "mention off"},       // ID "/trigger mention off"
	command.Button{Label: "Show all", Args: ""},                  // ID "/trigger"
	command.Button{Label: "Moderation", Command: "permission"},   // ID "/permission"
)
```

A menu is a list button: tapping its title opens the options, and picking one
works like a button tap. `/settings` sends one menu per part of the settings:

```go
return c.ReplyMenus(ctx, "Current: level 1", "Tap a menu to change a setting",
	command.Menu{Title: "Moderation", Options: []command.Button{
		{Label: "Level 0: off", Description: "I don't moderate.", Command: "permission", Args: "0"},
		{Label: "Level 1: delete", Command: "permission", Args: "1"},
	}},
)
```

- A message can have 1 to 10 buttons and menus in all, and a menu 1 to 10
  options. Labels and menu titles must not be empty.
- Send buttons only when someone asks for them (`/trigger toggle`,
  `/settings`). A command's plain view, and its answer to a tap, are text.
- `Args` must be valid input for the target command's `Run`. Add a test that
  parses every button you send (see
  `TestTriggerToggleShowsRulesWithButtonsThatRouteBackToTrigger`).
- When the host can't send buttons, or Discord can't show the layout (more
  than five rows, or a command longer than 100 bytes),
  `ReplyButtons` and `ReplyMenus` fall back to a text reply that lists each
  label next to the command to type.
- Buttons and menus are Discord message components. The adapter turns a tap
  on a button or a menu option into the user's message, so you never deal
  with provider types.

## Changing config

```go
updated, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
	values.Permission.ModerationLevel = level
})
```

- Set **absolute** values (`= level`), never relative ones (`++`, toggles
  based on the current value). After a crash, the command is replayed.
  `UpdateConfig` skips the write when running your function again would
  change nothing, and that check only works for absolute values.
  For a toggle, compute the target value when you parse the arguments, as
  `trigger.go` does with `on`/`off`.
- Validate first and reply with usage on bad input. `UpdateConfig` returns an
  `ErrorInvalidArgument` if the result fails config validation.
- Never call `c.Agent.Config().Set*` or `Update` directly. That bypasses
  the replay check.

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
2. Implement it in `internal/adapters/discord`.
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
- Don't import provider packages (`discordgo`, the adapter, and so on) in
  this folder.
- Keep replies in English, matching the existing commands.
