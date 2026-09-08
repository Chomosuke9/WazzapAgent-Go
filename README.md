# WazzapAgent Go

Greenfield rewrite WazzapAgent sebagai satu modular monolith Go. Part 1 menyediakan vertical slice text-only yang durable: pesan WhatsApp masuk, kebijakan eksternal, satu `Agent` per chat, LLM OpenAI-compatible, lalu pengiriman balasan melalui durable action outbox.

Status saat ini: implementasi Part 1 tersedia untuk verifikasi lokal. Canary akun WhatsApp nyata tetap harus dijalankan memakai account, data directory, port, dan allowlist khusus; project lama tidak disentuh.

## Bentuk OOP

`Agent` adalah aggregate chat-scoped untuk satu `(TenantID, AccountID, ChatID)`:

```go
current, err := registry.AgentFor(ctx, key)
snapshot, err := current.Config().Refresh(ctx)
result, err := current.Invoke(ctx, invocation)
```

- `Agent.Invoke` menjaga serialisasi per chat, idempotency turn, snapshot config, model invocation, dan dispatch dari action yang sudah disimpan.
- `Agent.Config()` adalah child object versioned dengan mutation CAS dan explicit change notification.
- `AgentRegistry` menjamin satu live Agent per key, coalescing concurrent construction, hard limit, dan idle eviction.
- Actor verification, trigger, allowlist, dan permission check sengaja berada di `internal/inbound` dan `internal/policy`, di luar object Agent.
- SQLite adalah source of truth; object/cache dapat direkonstruksi setelah restart.

Kontrak lengkap ada di [docs/rewrite/04-AGENT-CONTRACT.md](docs/rewrite/04-AGENT-CONTRACT.md).

## Part 1

Termasuk:

- fresh QR pairing dan persistent Hypermeow device session;
- eligible DM dan group mention text;
- durable dedup, opaque per-chat `senderRef`, generation lease, action outbox, receipt, dan restart recovery;
- `/prompt view`, `/prompt set <teks>`, dan `/prompt clear`, hanya untuk configured owner;
- fail-closed allowlist, external send reauthorization, response kill switch, bounded queues/concurrency/timeouts;
- `/health/live`, `/health/ready`, dan Prometheus text `/metrics` pada loopback secara default;
- scrub content terminal setelah 24 jam dan hapus terminal turn/action setelah 30 hari.

Belum termasuk history, media, tool calling, scheduler, sub-agent, control panel, multi-account product surface, atau stable production release.

## Mulai

Persyaratan: Go toolchain sesuai `go.mod`, satu dedicated test account WhatsApp, dan endpoint chat-completions yang OpenAI-compatible.

```text
copy .env.example .env
go test ./...
go build ./cmd/wazzapagent
```

`.env` tidak dibaca otomatis oleh binary. Ikuti [runbook Part 1](docs/rewrite/07-PART1-RUNBOOK.md) untuk mengisi dan memuat environment dengan aman, melakukan pairing dalam keadaan agent mati, mengaktifkan canary, memverifikasi, membackup, dan rollback.

## Dokumentasi

- [Master plan](PLAN.md)
- [Target architecture](docs/rewrite/02-TARGET-ARCHITECTURE.md)
- [Roadmap](docs/rewrite/03-ROADMAP.md)
- [Agent contract](docs/rewrite/04-AGENT-CONTRACT.md)
- [Testing and release](docs/rewrite/05-TESTING-RELEASE.md)
- [Part 1 operator runbook](docs/rewrite/07-PART1-RUNBOOK.md)
