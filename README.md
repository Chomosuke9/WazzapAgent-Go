# DiscordAgent Go

DiscordAgent Go is a complete rewrite of DiscordAgent: a self-hosted WhatsApp assistant with an AI Agent and a unified interface for managing conversations and settings.

## Features

- Connect and manage a linked WhatsApp account.
- Let the Agent respond in allowed direct chats and in groups when mentioned or replied to.
- Browse conversations, reply to messages, mention group members, and manage chat history.
- Configure prompts, access rules, group triggers, and per-chat behavior.
- Send broadcasts to multiple groups immediately or schedule them for later.
- View conversation activity and Agent usage in the dashboard and analytics pages.
- Use group moderation commands when the requester and the bot account have the required permissions.
- Make stickers with `/sticker`, and save named stickers with `/add-sticker` that the Agent can send in that chat.
- Keep conversation history and Agent actions durable across restarts.

## App modes

- **Windows desktop** — downloadable x64 application.
- **Linux desktop** — downloadable x64 application; GTK4 and WebKitGTK are required.
- **Android** — arm64 APK. The app stores its data in Android private storage. Android may stop the app process when its activity is closed, so continuous background operation is not guaranteed.
- **Browser** — run the local server and open the same interface in a browser. See the [browser guide](cmd/server/README.md). To host it on a server such as Debian behind an access token, use the [web host](cmd/webhost/README.md).

## Requirements

DiscordAgent needs a WhatsApp account and an OpenAI-compatible chat-completions endpoint. Configure the application with your endpoint and access settings before connecting WhatsApp.

## Data and supported content

The app starts building its conversation history when it receives messages; it does not import older WhatsApp history or automatically migrate data from the previous DiscordAgent application. Text conversations are supported; the Agent sees images, videos and stickers only as text markers. `/sticker` turns images and stickers into stickers on every platform; turning videos and GIFs into stickers needs [ffmpeg](https://ffmpeg.org) on the `PATH` of the computer running the app.

## Downloads and builds

See the [build and release guide](build/README.md) for desktop packages and release assets, and the [Android build guide](build/android/README.md) for Android packages.

## Platform documentation

See the [multiplatform guide](docs/multiplatform/README.md) and its [manual testing guide](docs/multiplatform/TESTING.md) for platform-specific details.
