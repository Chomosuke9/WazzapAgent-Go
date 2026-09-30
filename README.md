# DiscordAgent Go

DiscordAgent Go is a self-hosted Discord bot with an AI Agent and one interface for managing conversations and settings. It is a Discord port of WazzapAgent Go.

## Features

- Link a Discord bot with its token and keep it online.
- Let the Agent respond in allowed direct messages, and in server channels and threads when it is mentioned or replied to (or by name, or through smart rules).
- Browse conversations, reply to messages, mention members, and manage chat history.
- Configure prompts, access rules, triggers, and per-chat behavior.
- Broadcast to several channels at once, now or on a schedule, as plain text or as a Discord message JSON with embeds and components.
- View conversation activity and Agent usage in the dashboard and analytics pages.
- Moderate with `/mod` (lock or unlock a channel, set its topic, delete messages, mute, kick) when the requester is a server moderator and the bot has the Discord permission for the action.
- Answer buttons and menus as Discord message components; the Agent can send the server's own stickers.
- Keep conversation history and Agent actions durable across restarts.

## Requirements

- A Discord application with a bot user, created in the [Discord Developer Portal](https://discord.com/developers/applications). On its **Bot** page, turn on **Message Content Intent**; turn on **Server Members Intent** too if you want the member list in Chat.
- An OpenAI-compatible chat-completions endpoint.

## Getting started

1. Start the app and open the **Discord** page. Paste the bot token and select **Link bot**. The token is verified with Discord and saved only on the device, readable by your user account.
2. Use the invite link on that page to add the bot to your servers. It asks for the permissions the bot uses: read and send messages, react, manage messages, manage channels and permissions (for `/mod lock` and `/mod topic`), and kick members.
3. In **Settings**, set the owner (your Discord user ID) and the allowlist, then start the Agent from **Overview**.

Discord IDs are numbers: turn on Developer Mode in Discord's settings, then right-click a user, channel or server and choose **Copy ID**. An allowlist entry can be a channel, thread, server or user ID, or a wildcard: `*` (every chat), `server:*` (every server channel), or `dm:*` (every direct message). A server ID admits all its channels; a user ID admits direct messages with that user.

The command-line build reads its settings from the environment or `.env` (see [.env.example](.env.example)), including the bot token in `DISCORDAGENT_DISCORD_TOKEN`.

## App modes

- **Windows desktop** — downloadable x64 application.
- **Linux desktop** — downloadable x64 application; GTK4 and WebKitGTK are required.
- **Android** — arm64 APK. The app stores its data in Android private storage. Android may stop the app process when its activity is closed, so continuous background operation is not guaranteed.
- **Browser** — run the local server and open the same interface in a browser. See the [browser guide](cmd/server/README.md). To host it on a server such as Debian behind an access token, use the [web host](cmd/webhost/README.md).
- **Command line** — `go build ./cmd/discordagent` runs the Agent headless from environment settings.

## Data and supported content

The app starts building its conversation history when it receives messages; it does not import older Discord history. Text conversations are supported; the Agent sees attachments and stickers only as text markers such as `【image】`. Replies longer than Discord's 2000-character limit are sent as several messages.

Muting with `/mod mute` is enforced by the bot: it deletes the muted member's new messages in that channel, so it needs the Manage Messages permission.

## Downloads and builds

See the [build and release guide](build/README.md) for desktop packages and release assets, and the [Android build guide](build/android/README.md) for Android packages.

## Platform documentation

See the [multiplatform guide](docs/multiplatform/README.md) and its [manual testing guide](docs/multiplatform/TESTING.md) for platform-specific details.
