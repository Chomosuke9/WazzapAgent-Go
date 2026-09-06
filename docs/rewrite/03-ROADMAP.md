# Roadmap dan Work Breakdown

Project ini greenfield. Source lama dipakai untuk inventaris fitur dan test ideas, bukan sebagai runtime, data source, atau compatibility target.

## Milestone 0 — Product scope dan technical decisions

### Tujuan

Menetapkan release scope sebelum coding besar.

### Work items

- Klasifikasikan fitur di baseline sebagai `required`, `deferred`, atau `rejected`.
- Tentukan canonical message, action, result, dan stable error model baru.
- Tetapkan command list release pertama.
- Tentukan API versioning dan authentication model control panel.
- Buat native WhatsApp capability test matrix.
- Tutup ADR SQLite driver/CGO, database topology, process topology, config reload, dan deployment target.
- Definisikan SLO awal dan resource limits.

### Exit gate

- Scope v1 disetujui.
- Semua P0/P1 feature memiliki acceptance criteria.
- Critical ADR selesai.
- Tidak ada requirement import data, auth, atau protocol lama.

## Milestone 1 — Fondasi aplikasi

### Tujuan

Menyediakan executable Go yang reproducible dan operable.

### Work items

- Buat `cmd/wazzapagent` dan composition root.
- Implement typed config, defaults, validation, immutable snapshots, dan secret redaction.
- Implement structured logger dan instance ID.
- Tambah liveness/readiness endpoints.
- Tambah lifecycle context, graceful shutdown, dan goroutine tracking.
- Track `go.sum`; pin toolchain/dependencies.
- Rapikan prototype `pkg/whatsapp/socket.go` menjadi adapter spike atau hapus setelah tercakup.
- Siapkan CI untuk format, vet, test, race, vulnerability scan, dan build.

### Exit gate

```text
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
```

Binary start dengan config minimal, invalid config gagal sebelum network startup, health endpoints benar, dan SIGTERM selesai tanpa leak.

Dependency: Milestone 0.

## Milestone 2 — Persistence v1

### Tujuan

Membangun storage baru dengan schema dan ownership jelas sejak awal.

### Work items

- Tenant/account schema dan data root layout.
- Embedded immutable migrations dengan version/checksum.
- Repositories untuk settings, directory, models/providers, activation, memories, moderation, stats, stickers/media.
- Tables untuk action receipts, inbox/outbox, audit, jobs, dan sub-agent.
- WAL/foreign key/busy/checkpoint configuration.
- Transaction boundaries dan repository interfaces.
- Backup, restore, integrity-check, dan retention commands.

### Exit gate

- Fresh DB mencapai schema v1 deterministically.
- Migration rerun idempotent dan checksum mismatch fail-fast.
- Two-tenant isolation tests lulus.
- Crash/restart tidak merusak committed transactions.
- Backup dapat direstore dan dibuka oleh release yang sama.

Dependency: Milestone 1.

## Milestone 3 — Native WhatsApp adapter

### Tujuan

Membuktikan konektivitas native Go untuk account baru sebelum agent dibangun di atasnya.

### Work items

- Per-tenant hypermeow device store.
- QR dan pairing-code flows.
- Connect/reconnect/logout/device removal lifecycle.
- Canonical event normalization.
- Text send/receive, reply, mention, reaction, delete, read, presence.
- Group metadata, participant roles, kick.
- Media download/upload.
- Capability flags dan typed unsupported errors.
- Per-chat/JID send serialization dan reconnect backoff.

### Exit gate

- Fresh account dapat pair dan reconnect setelah process/host restart.
- DM/group basic actions lulus pada real devices.
- Multi-account isolation lulus.
- Network loss, logout, stream conflict, dan cancellation tests lulus.
- Unsupported P1 capability memiliki fallback atau dikeluarkan eksplisit dari v1.

Dependency: Milestone 1–2.

## Milestone 4 — Message domain dan command core

### Tujuan

Membangun normalized message pipeline dan user-facing non-LLM features.

### Work items

- Message unwrap, stable context IDs, quoted lookup, sender refs.
- LID/phone JID resolution.
- Mention/tag-all/replied-to-bot detection.
- Group metadata cache dan stampede protection.
- Command/button registries dengan explicit registration.
- Owner/admin/superadmin/user permissions.
- Activation, mute, delete, kick, settings, model, memory, help.
- Media and sticker catalog/conversion.
- Interactive messages sesuai capability matrix.

### Exit gate

- Golden normalization fixtures lulus.
- Context lookup dan cache bounds teruji.
- Permission/activation tidak dapat dibypass.
- Required v1 commands lulus integration tests.
- Media path, MIME, size, timeout, dan cleanup tests lulus.

Dependency: Milestone 2–3.

## Milestone 5 — Agent core

### Tujuan

Membangun end-to-end LLM response pipeline.

### Work stream A: history/context

- Message history model dan bounded retention.
- Quoted hydration dan assistant identity.
- Deterministic history serialization.
- Context injection guards.
- Settings/model/memory context builder.

### Work stream B: batching

- Per-chat queue/lock.
- Debounce, burst cap, stale context-only policy.
- Prefix interrupt dan cancellation.
- Idle trigger, mute gate, reply dedup.

### Work stream C: LLM

- OpenAI-compatible provider abstraction.
- LLM1 typed routing/tools.
- LLM2 prompt/tools/result validator.
- Primary/fallback, timeouts, retries, multimodal fallback.
- Fake deterministic provider.

### Work stream D: durable actions

- Typed action extraction and validation.
- Permission check immediately before side effect.
- Transactional action receipt and outbox.
- Unknown-outcome reconciliation state.
- History finalization from normalized send result.

### Exit gate

- Golden prompt/tool/action tests lulus.
- Per-chat ordering dan cross-chat concurrency benar.
- Duplicate request tidak mengulang local side effect.
- Timeout, fallback, cancellation, malformed output, and crash injection lulus.
- End-to-end DM/group response bekerja pada test accounts.

Dependency: Milestone 2–4.

## Milestone 6 — Jobs, direct invoke, dan sub-agent

### Tujuan

Menambah workflows asynchronous dengan durability lengkap.

### Work items

- Shared ChatReinvoker.
- One-shot dan daily scheduler dengan transactional leases.
- Direct invoke HTTP dengan fail-closed auth dan async acceptance.
- Sub-agent client, retries, steering, resumable upload.
- Authenticated webhook dan progress keepalive.
- Durable tracker, deferred completion, output spool, delivery checkpoints.
- Recovery, retry, discard, dan dead-letter operations.

### Exit gate

- Past-due, shutdown cancellation, recurrence, timezone, dan lease expiry tests lulus.
- Cold work menunggu account `open` tanpa kehilangan job.
- Webhook di-ACK hanya setelah durable ownership.
- Crash pada setiap state transition tidak kehilangan atau menggandakan completion.
- File size/hash/traversal and duplicate delivery tests lulus.

Dependency: Milestone 5.

## Milestone 7 — Control panel

### Tujuan

Menyediakan seluruh operasi v1 melalui secure web interface/API.

### Work items

- Initial secure token setup dan auth middleware.
- Account add/pair/reconnect/logout/disable/delete-data workflow.
- Settings, models/providers, activation, memories, moderation, stickers.
- Jobs and sub-agent administration.
- Secret-safe config editor.
- Audit events, health, metrics, backup/restore.
- Embedded static UI.

### Exit gate

- Auth fail-closed, timing-safe comparison, rate limiting, and security headers lulus.
- Tenant-scoped authorization/path tests lulus.
- Semua mutations transactional dan audited.
- Pairing dan representative management workflows lulus browser/API tests.

Dependency: Milestone 2–6. UI dapat dimulai paralel setelah API contracts stabil.

## Milestone 8 — Advanced WhatsApp features

### Tujuan

Menyelesaikan fitur adapter yang kompleks tanpa menghambat runtime inti.

### Work items

- Buttons, carousel, copy-code, quiz.
- Lottie and animated sticker handling.
- View-once, ephemeral, edited, and uncommon protocol wrappers.
- Broadcast/announcement behavior.
- Device compatibility fallbacks.
- Real-device matrix Android/iOS/Web.

### Exit gate

- Setiap required feature lulus real-device tests.
- Unsupported features memiliki UX fallback yang aman.
- Reconnect tidak menyebabkan duplicate interactive sends.
- Media/resource bounds tetap terpenuhi.

Dependency: Milestone 3–5. Dapat paralel dengan Milestone 6–7.

## Milestone 9 — Hardening dan release candidate

### Tujuan

Menyiapkan release baru dari fresh environment.

### Work items

- End-to-end fresh install dan pairing.
- Multi-account load/soak/fault tests.
- Security review dan `govulncheck`.
- Backup/restore and schema-upgrade drill.
- Reproducible target builds/checksums.
- Container atau systemd/Pterodactyl packaging.
- Operator runbook, alerts, capacity, and retention defaults.
- License/dependency inventory.

### Exit gate

- Semua required feature gates lulus.
- Zero tenant-isolation violation.
- No unreconciled duplicate destructive action.
- 24+ hour soak memenuhi error/resource budget.
- Restore and host-restart recovery teruji.
- Release artifact berjalan tanpa Node/Python.

Dependency: Milestone 1–8 required scope.

## Critical path

```text
M0 scope
  -> M1 foundation
  -> M2 persistence
  -> M3 native WhatsApp
  -> M4 domain/commands
  -> M5 agent
  -> M6 background workflows
  -> M7 control panel
  -> M9 release candidate
```

M8 dapat berjalan paralel setelah native adapter dan agent core stabil. Fitur M8 yang berstatus deferred tidak memblokir v1.
