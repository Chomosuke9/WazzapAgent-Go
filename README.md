# WazzapAgent Go

WazzapAgent Go is a complete rewrite of WazzapAgent: a self-hosted WhatsApp assistant with an AI Agent and a unified interface for managing conversations and settings.

## Features

- Connect and manage a linked WhatsApp account.
- Let the Agent respond in allowed direct chats and in groups when mentioned or replied to.
- Browse conversations, reply to messages, mention group members, and manage chat history.
- Configure prompts, access rules, group triggers, and per-chat behavior.
- Send broadcasts to multiple groups immediately or schedule them for later.
- View conversation activity and Agent usage in the dashboard and analytics pages.
- Use group moderation commands when the requester and the bot account have the required permissions.
- Keep conversation history and Agent actions durable across restarts.

## App modes

- **Windows desktop** — downloadable x64 application.
- **Linux desktop** — downloadable x64 application; GTK4 and WebKitGTK are required.
- **Android** — arm64 APK. The app stores its data in Android private storage. Android may stop the app process when its activity is closed, so continuous background operation is not guaranteed.
- **Browser** — run the local server and open the same interface in a browser. See the [browser guide](cmd/server/README.md).

## Requirements

WazzapAgent needs a WhatsApp account and an OpenAI-compatible chat-completions endpoint. Configure the application with your endpoint and access settings before connecting WhatsApp.

## Data and supported content

The app starts building its conversation history when it receives messages; it does not import older WhatsApp history or automatically migrate data from the previous WazzapAgent application. Text conversations are supported. Stickers appear as text markers, and media processing is not available.

## Downloads and builds

See the [build and release guide](build/README.md) for desktop packages and release assets, and the [Android build guide](build/android/README.md) for Android packages.

## Platform documentation

See the [multiplatform guide](docs/multiplatform/README.md) and its [manual testing guide](docs/multiplatform/TESTING.md) for platform-specific details.
