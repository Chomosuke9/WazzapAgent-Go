# Changelog

## v0.1.0 (2026-10-17)

The first DiscordAgent release: a Discord port of
[WazzapAgent-Go](https://github.com/Chomosuke9/WazzapAgent-Go), based on its
`main` at `0294eb9`. Everything WhatsApp-specific is removed. The Agent, its
commands, the control panel, and storage carry over.

### Discord

- **Bot linking.** Paste a bot token on the new **Discord** page, which
  replaces WhatsApp pairing. The token is checked with Discord, stored only on
  the device, and never logged or shown again. The page has the invite link
  with the permissions the bot needs, and shows why a link failed.
- **Where the Agent replies.** Direct messages, server channels, and threads.
  In channels it wakes on an @mention, a reply to it, its name, or smart rules,
  as in groups before.
- **Roles.** Server moderators count as admins: members with any moderation
  permission in the channel (Manage Messages, Manage Channels, Manage
  Server, Kick, Ban, or Timeout Members). The server owner and members with
  Administrator count as superadmin.
- **Allowlist.** Entries are Discord IDs: a channel, thread, server (all its
  channels), or user (direct messages with them). Wildcards are `*`,
  `server:*` and `dm:*`.
- **Owner.** Set by Discord user ID (`DISCORDAGENT_OWNER_ID`).
- **Replies.** Messages longer than 2,000 characters are split across
  several messages. Answer buttons and menus are Discord components.
- **Stickers.** The Agent can send the server's own stickers.
- **Broadcast.** Plain text, or a Discord message JSON with embeds and
  components.
- **Channel names.** They stay current in the control panel and refresh after
  the bot reconnects.

### Commands

- **`/mod` replaces `/group`.** Its actions are `lock`/`unlock` the channel,
  set its `topic`, `delete` a message, `mute` a member, and `kick` a member.
  It is for server moderators, and each action needs the matching Discord
  permission. Muting is enforced by the bot, which deletes the muted member's
  new messages in that channel.
- **`/memory` (new).** The bot keeps up to 50 notes per chat, each up to 500
  characters, and sees them on every turn. It saves notes on its own. The
  owner, server moderators, and anyone in a direct message can list, add, or
  delete them by hand.
- **Removed:** `/sticker`, `/add-sticker`, and `/remove-sticker`. Server
  stickers are managed in Discord's server settings.

### Agent

- **Self-management tiers.** The system prompt now sorts what the bot may
  change. Memory and one-off reminders are its own call. Daily tasks and chat
  settings need a moderator to ask. In a server channel, the bot may also
  propose a change and apply it once a moderator agrees.
- **Answer buttons** are used sparingly: only for requested quizzes or polls,
  or for real decisions, and never on two replies in a row.

### Settings and data

- **Environment prefix.** Variables are `DISCORDAGENT_*` instead of
  `WAZZAP_*`, and the bot token goes in `DISCORDAGENT_DISCORD_TOKEN`. See
  `.env.example`.
- **Data location.** Data lives in its own `discordagent` directory, and the
  database starts from a single new schema. WazzapAgent data is not migrated.

### Builds and maintenance

- **Builds.** Windows and Linux desktop apps, an Android APK, a web host for
  Linux (amd64 and arm64), and the headless `discordagent` command.
- **Toolchain.** Go 1.26.9, plus patched OpenTelemetry, gRPC, and
  `golang.org/x` modules.
- **Upstream sync.** The Go module path stays upstream's so upstream updates
  merge cleanly. See [Syncing with upstream](docs/upstream-sync.md).
