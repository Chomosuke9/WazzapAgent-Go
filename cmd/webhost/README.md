# Web host (`discordagent-web`)

`cmd/webhost` hosts the browser UI for other machines, for example a Debian
server. It runs the same Agent, Discord bot session and settings as the desktop app
and serves the same pages as [`cmd/server`](../server/README.md), but it listens
on a network address and requires an **access token**.
`cmd/server` stays the loopback-only, no-login option for a single machine.

Run only one app or CLI process against a data root at a time.

## Build

Go 1.26.5+ and the Node/npm versions in `frontend/package.json`:

```sh
task webhost:build
# equivalent:
npm --prefix frontend ci && npm --prefix frontend run build:web
CGO_ENABLED=0 go build -tags web -trimpath -ldflags "-s -w" -o bin/discordagent-web ./cmd/webhost
```

`CGO_ENABLED=0` gives a static binary. Cross-compile for a Debian server with
`GOOS=linux GOARCH=amd64` (or `arm64`). The build embeds the UI, so the binary
is all that has to be copied.

## Access token

On first start the server creates a random token, prints it once, and saves it
to `~/.config/discordagent/web-token` (mode 0600, next to `bootstrap.json`).

1. Open `http://<server>:8080`, paste the token, sign in.
2. The server replies with a session cookie (HttpOnly, SameSite=Strict, valid
   180 days). The browser keeps it, so you do not enter the token again, and the
   token itself is never stored in the page.

```sh
discordagent-web token           # print the token (works while the service runs)
discordagent-web token -rotate   # new token; restart the service to apply
```

Sessions are signed with a key derived from the token, so they survive server
restarts, and rotating the token signs out every browser. **Sign out** in the
sidebar removes the cookie from the current browser. To choose your own token
(16+ characters) set `DISCORDAGENT_WEB_TOKEN`; it takes precedence over the file.

After 10 wrong tokens from one address the login is refused for 15 minutes.

## Options

| Flag | Environment | Default |
| --- | --- | --- |
| `-addr` | `DISCORDAGENT_WEB_ADDR` | `0.0.0.0:8080` |
| `-public-origin` | `DISCORDAGENT_WEB_PUBLIC_ORIGIN` | none |
| `-tls-cert`, `-tls-key` | `DISCORDAGENT_WEB_TLS_CERT`, `DISCORDAGENT_WEB_TLS_KEY` | none (plain HTTP) |

## HTTPS

The token is sent when you sign in, so do not use plain HTTP across an
untrusted network. Either:

- serve HTTPS directly with `-tls-cert` and `-tls-key`; or
- put a TLS reverse proxy (Caddy, nginx) in front, forward to the listen
  address, preserve the `Host` header, and set
  `DISCORDAGENT_WEB_PUBLIC_ORIGIN=https://your-host.example`. If the proxy is on the
  same machine, also set `DISCORDAGENT_WEB_ADDR=127.0.0.1:8080`.

The session cookie is marked `Secure` whenever the browser reached the server
over HTTPS. Requests must be same-origin, and `X-Forwarded-For` is not trusted,
so behind a proxy the login rate limit applies to all clients together.

## systemd

[`deploy/discordagent-web.service`](../../deploy/discordagent-web.service) is a
hardened unit that runs the server as a dedicated `discordagent` user; the
install steps are in its header. Read the first token from
`journalctl -u discordagent-web` or with the `token` command.
