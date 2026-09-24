# Browser UI

`cmd/server` serves the same React pages as the Wails desktop/mobile shell. The
browser build swaps Wails calls for a same-origin JSON bridge. Both transports
call the same control use cases and use the same settings, WhatsApp session, and
Agent runtime data. Run only one app or CLI process against a data root at a time.

Build from the repository root with Go 1.26.5+ and the Node/npm versions in
`frontend/package.json`:

```sh
wails3 task web:build
wails3 task web:run
```

Open `http://127.0.0.1:8080`. Set `WAZZAP_WEB_ADDR=127.0.0.1:PORT` to choose a
different loopback port. The server rejects non-loopback listeners and browser
requests with a non-loopback Host or cross-site Origin. To use it from another
device, connect through an SSH tunnel or an authenticated HTTPS reverse proxy.
For the proxy, set `WAZZAP_WEB_PUBLIC_ORIGIN=https://your-host.example`,
preserve the browser's `Host` header, and require authentication at the proxy;
the server still listens only on loopback.
Do not publish the port directly.

The browser polls WhatsApp session status while the page is open. The server and
Agent continue running when the browser closes; stopping the server shuts down
the Agent and session and preserves their data. Browser mode is independent of
native Android packaging and background service work.
