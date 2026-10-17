# Syncing with upstream

DiscordAgent-Go is a fork of
[WazzapAgent-Go](https://github.com/Chomosuke9/WazzapAgent-Go) with the
WhatsApp parts removed and a Discord adapter in their place. The shared core
(agent, inbound, commands, policy, SQLite store, UI) still follows upstream,
so its fixes and features are worth merging.

The Go module path is deliberately upstream's,
`github.com/Chomosuke9/WazzapAgent-Go`, even though the product is
DiscordAgent. Every Go file's import block then matches upstream, and an
upstream change to imports merges cleanly. Keep it that way. The same goes
for the generated Wails bindings under
`frontend/bindings/github.com/Chomosuke9/WazzapAgent-Go`.

## Merge

Always merge; never rebase, and never use "Discard commits" on GitHub's
"Sync fork" button: it drops the whole Discord port.

```sh
git remote add upstream https://github.com/Chomosuke9/WazzapAgent-Go  # once
git fetch upstream main
git checkout -b sync-upstream main
git merge upstream/main
```

Resolve what conflicts as below, run the checks, then open a pull request
into `main`.

## Resolving

| Upstream path | In this fork | On conflict |
| --- | --- | --- |
| `internal/adapters/whatsapp/**` | `internal/adapters/discord` | `git rm` it. Port the behavior to the Discord adapter by hand if Discord needs it. |
| `internal/sticker/**`, `commands/sticker*.go`, `commands/add-sticker*`, `commands/remove-sticker*`, `sqlite/stickers*` | none: server stickers are managed in Discord and listed by `send_sticker` | `git rm` it. |
| `internal/inbound/commands/group*.go` | `commands/mod*.go` (`/mod`) | `git rm` it; port the change into `mod.go`. |
| `internal/adapters/sqlite/group_metadata*` | none: channel names come from Discord | `git rm` it. |
| `cmd/wazzapagent/` | `cmd/discordagent/` | `git rm` it; port the change. |
| `frontend/src/pages/WhatsAppPage.tsx` | `frontend/src/pages/DiscordPage.tsx` | `git rm` it; port the change. |
| `deploy/wazzapagent-web.service` | `deploy/discordagent-web.service` | `git rm` it; port the change. |
| `internal/adapters/sqlite/migrations/NNN_*.sql` | see below | Never take as is. |
| `frontend/bindings/**` | generated | Take either side, then regenerate (see Checks). |
| `internal/app/systemprompt.txt`, command replies | Discord wording | Keep the Discord terms: server channel, moderator, `/mod`, `**bold**`. |

Upstream adds WhatsApp-only fields too (for example media payloads on
`conversation.IncomingCandidate`). Leave out what only WhatsApp fills.

### Migrations

This fork's schema starts from a single `001_schema.sql`; upstream's
numbered migrations (`002` to `027` and later) are already folded into it.
An upstream migration file must never be merged as is: the runner would
apply it on top of `001`, and it may assume WhatsApp tables.

- Until the bot has been run against a database you want to keep, fold the
  change into `001_schema.sql`.
- After that, never edit an applied migration: the store checks each
  file's checksum and refuses to open on a mismatch. Write the change as
  this fork's next file, `002_<name>.sql`, `003_<name>.sql` and so on, and
  update `wantMigrationCount` in `internal/adapters/sqlite/store_test.go`.

## Checks

```sh
wails3 generate bindings -f '-tags gui' -clean=true -ts -i ./cmd/app
gofmt -l ./internal ./cmd
go vet ./... && go vet -tags gui ./... && go vet -tags web ./...
go test -race ./...
(cd frontend && npm run typecheck && npm run build && npm run build:web)
```
