# Roadmap dan Work Breakdown

Estimasi tidak memakai minggu kalender karena kapasitas tim belum diketahui. Setiap phase memiliki dependency dan exit gate; phase berikutnya tidak boleh memakai status “hampir selesai” sebagai dependency.

## Milestone 0 — Bekukan baseline

### Tujuan

Membuat perilaku lama dapat diukur sebelum ada replacement.

### Work items

- Inventaris semua protocol frames dari docs, TS, Python, dispatcher, dan tests.
- Buat canonical schema untuk frame/action/event dan error codes.
- Tambah `render_html`, `download_media`, nullable/pending attachment, dan actual delivery semantics.
- Rekam golden JSON untuk setiap frame sukses/gagal dan malformed input.
- Snapshot semua control panel route, request, response, status code, dan auth behavior.
- Snapshot hasil schema SQLite fresh dan upgraded.
- Rekam golden prompt/messages/tool schemas/action extraction untuk LLM1/LLM2.
- Rekam message normalization fixtures dari representative Baileys events.
- Bentuk CI baseline untuk Node typecheck/tests dan Python lint/tests.

### Exit gate

- Tidak ada runtime action yang hilang dari schema.
- TS dan Python dapat membaca semua golden frame.
- Schema dan HTTP snapshot committed.
- Existing test suite hijau atau known failures didokumentasikan.
- Setiap known contract drift memiliki keputusan.

## Milestone 1 — Fondasi Go

### Tujuan

Menyediakan executable yang reproducible tanpa mengambil alih production behavior.

### Work items

- Buat `cmd/wazzapagent` dan lifecycle context.
- Implement config typed, defaults, validation, secret redaction, dan restart metadata.
- Implement structured logger dan instance ID.
- Tambah live/ready endpoints.
- Track `go.sum`; tambahkan lint/test/race/build commands ke repository guidance.
- Pin toolchain dan dependency versions.
- Tutup ADR SQLite driver/CGO, config hot reload, dan single-binary versus service split sebelum package boundary dibekukan.
- Rapikan prototype `pkg/whatsapp/socket.go` ke adapter/spike atau hapus setelah fungsinya tercakup.

### Exit gate

```text
go test ./...
go test -race ./...
go vet ./...
```

Binary start, health endpoint lulus, invalid config gagal sebelum membuka socket, dan SIGTERM selesai tanpa goroutine leak.

Dependency: Milestone 0 schema minimal.

## Milestone 2 — Protocol SDK dan compatibility harness

### Tujuan

Go dapat berbicara protocol v2 tanpa side effect WhatsApp.

### Work items

- Typed frame envelope, custom validation, error mapping.
- Request ID generation/correlation dan timeout behavior.
- Hello/auth/heartbeat/reconnect state machine.
- Reliable bounded queue dan overflow metrics.
- Mock WS server/client untuk Node dan Python compatibility.
- Replay seluruh golden fixtures ke Go decoder/encoder.
- Explicit adapter untuk legacy top-level control frames.
- Tutup ADR raw Baileys action result: normalized protocol baru dan serializer compatibility v2.

### Exit gate

- Go ↔ Node dan Go ↔ Python handshake lulus.
- Semua action/event round-trip field-identical sesuai canonical schema.
- Reconnect tidak reorder reliable frames.
- Timeout dan late ACK behavior parity.
- Queue overflow deterministic dan observable.

Dependency: Milestone 0–1.

## Milestone 3 — Persistence read-only dan migrations framework

### Tujuan

Membaca semua state existing dengan aman tanpa menjadi writer ketiga.

### Work items

- Tenant path resolver dan stable TenantID mapping.
- Open existing split DB read-only.
- Typed repositories untuk settings, models, activation, memories, stats, moderation, stickers, jobs.
- Verify SQLite pragmas, timestamp, NULL, default, `__global__`, dan ordering semantics.
- Embedded versioned migrations dengan dry-run dan checksum, tetapi writes disabled.
- Schema diff command dan backup verifier.
- Concurrency tests dengan Node/Python writers pada copied fixtures.

### Exit gate

- Query outputs cocok dengan implementations lama pada fixture DB.
- Dua tenant dengan chat ID sama tidak berbagi data.
- No schema mutation pada read-only mode.
- Semua historical fixture dapat diperiksa dan migration plan deterministik.

Dependency: Milestone 1.

## Milestone 4 — Control plane Go

### Tujuan

Memindahkan HTTP control panel dan account catalog terlebih dahulu, dengan adapters ke runtime lama.

### Work items

- Auth, rate limiter, security headers, body limits.
- Account catalog CRUD dengan atomic writes dan stable IDs.
- Overview/account runtime status adapter.
- Settings, memory, model, activation, bot config, stickers CRUD.
- Environment editor dengan secret masking dan restart flags.
- Audit persistence.
- Sub-agent outbox admin proxy.
- Optional update/restart adapter; tidak menjadi core requirement binary.
- Static UI parity atau reverse proxy UI lama sementara.

### Exit gate

- Black-box HTTP contract tests lulus terhadap server lama dan Go.
- Semua mutations audited.
- Unauthorized, path traversal, oversized body, malformed JSON, dan rate limit tests lulus.
- Account removal mempertahankan tenant data.

Dependency: Milestone 2–3. Pair/reconnect boleh tetap diproxy ke Node.

## Milestone 5 — Agent core Go dalam shadow mode

### Tujuan

Mengganti Python bridge behavior tanpa mengirim action nyata.

### Work stream A: history dan context

- Message model, quoted hydration, sender refs, assistant identity.
- Exact history serialization dan injection guards.
- Settings/model/memory context builder.
- Tutup ADR durability/retention `contextMsgId` sebelum history implementation dibekukan.

### Work stream B: batching

- Per-chat queues dan locks.
- Debounce, burst cap, stale context-only behavior.
- Prefix interrupt/cancellation.
- Idle trigger, mute gate, reply dedup.

### Work stream C: LLM

- OpenAI-compatible client dan provider chain.
- LLM1 tools/decision parsing.
- LLM2 prompt order, tools, validator, vision/text fallback.
- Fake deterministic providers.

### Work stream D: action intent

- Extract legacy markers dan tool calls.
- Permission/activation checks.
- Implement `ShadowGateway` yang memenuhi canonical gateway port, merekam intent/diff, dan menolak network serta production writes.
- Compare Go action intents dengan Python, tanpa dispatch.

### Exit gate

- Golden prompt fixtures byte-equivalent atau intentional deltas di-ADR-kan.
- Replay corpus menghasilkan routing/action intents setara.
- Per-chat ordering sama; chat berbeda concurrent.
- Cancellation, timeouts, fallback, dedup, dan malformed model output lulus.
- Shadow mode tidak memiliki side effect.

Dependency: Milestone 2–3.

## Milestone 6 — Jobs, direct invoke, dan sub-agent

### Tujuan

Memindahkan cold/background workflows beserta durability semantics.

### Work items

- Shared ChatReinvoker.
- One-shot scheduler dan daily scheduler.
- Transactional job claims dan restart recovery.
- Direct invoke HTTP, fail-closed auth, async 202 behavior.
- Sub-agent client, retry, steering, resumable upload.
- Webhook auth, progress keepalive, completion durability.
- Tracker state machine, deferred completion, checkpointed delivery, output spool.
- Recovery dan admin outbox.
- Buat writable store ownership matrix per table/file: Go hanya boleh menulis table baru yang Go-owned untuk shadow/durable staging; table dan JSON milik Node/Python tetap read-only sampai Milestone 7.

### Exit gate

- Ownership matrix disetujui dan diuji; Milestone 6 tidak menulis table/file yang masih dimiliki Node/Python.
- Crash pada setiap state transition tidak kehilangan atau menggandakan completion.
- Past-due jobs, shutdown cancellation, daily recurrence, dan timezone DST cases diuji.
- Cold work menunggu WhatsApp open tanpa menghapus task.
- Webhook di-ACK hanya setelah durable ownership.
- File size/hash/path traversal tests lulus.

Dependency: Milestone 5. Writable store ownership matrix merupakan deliverable pertama Milestone 6 sebelum implementation yang menulis state.

## Milestone 7 — Go menjadi agent dan migration owner

### Tujuan

Cutover Python bridge tenant-by-tenant dan hentikan dual schema ownership.

### Work items

- Aktifkan Go action dispatch melalui Node/Baileys sidecar.
- Transactional action inbox/receipts dan outbox.
- Migrasi `action-receipts.json` dan `subagent_tracker.json` secara idempotent.
- Enable repository writes setelah backup dan schema lock.
- Hentikan Python untuk canary tenant.
- Compare metrics, replies, actions, job state, dan DB writes.
- Expand canary bertahap.
- Tetapkan auth ownership untuk fase ini: Node sidecar tetap menjadi pemilik auth dan Go tidak menyentuh `<tenant>/auth`.

### Exit gate

- Tidak ada Python writer untuk tenant yang sudah cutover.
- Node sidecar auth ownership dan rollback path terdokumentasi; native auth tetap terpisah sampai Milestone 8–9.
- Duplicate replay setelah process crash tidak mengulang side effect.
- Rollback ke Python/Node telah diuji pada backup copy dan canary.
- Minimal 24 jam burn-in per stage; durasi final disepakati dari traffic nyata.

Dependency: Milestone 4–6.

## Milestone 8 — WhatsApp native Go spike

### Tujuan

Menentukan apakah `hypermeow` dapat mengganti Baileys tanpa asumsi.

### Spike matrix

- QR dan pairing number flow.
- Fresh login, reconnect, logout, device removal.
- Existing Baileys auth import; jika tidak mungkin, explicit re-pair UX.
- Private/group messages, LID/PN mapping, participant roles.
- Quoted/reply, mentions/tag-all, reaction, delete, kick.
- Image/video/audio/document/sticker download dan upload.
- View-once/ephemeral/edited/protocol wrappers.
- Buttons, carousel, copy-code, quiz, Lottie, raw relay equivalents.
- Group metadata update and cache behavior.
- Network loss, conflict, rate-limit, stream replaced, device logout.
- Multi-account resource isolation.

### Exit gate

Buat ADR memilih salah satu:

1. sidecar Node dipertahankan;
2. native Go hanya untuk subset tenants/features;
3. native Go siap canary penuh.

Native Go tidak boleh lanjut hanya karena basic QR/send text berhasil.

Dependency: dapat dimulai paralel setelah Milestone 1, tetapi production cutover setelah Milestone 7 stabil.

## Milestone 9 — Gateway native canary

### Tujuan

Mengganti Node/Baileys untuk tenant yang memenuhi readiness matrix.

### Work items

- Implement canonical gateway adapter native.
- Import atau re-pair session dengan backup.
- Side-by-side event normalization comparison pada test accounts.
- Canary private chat, lalu test group, lalu selected production tenant.
- Feature flags per tenant untuk adapter selection.
- Rollback adapter tanpa DB downgrade.

### Exit gate

- Semua required capability matrix lulus pada real Android/iOS/Web participants yang disepakati.
- No message loss/duplication pada reconnect tests.
- Interactive/media behavior diterima.
- Rollback time objective tercapai.

Dependency: Milestone 7–8.

## Milestone 10 — Deployment dan cleanup

### Tujuan

Menyelesaikan replacement dan menghapus compatibility code secara terkontrol.

### Work items

- Reproducible Linux/Windows builds dan artifacts/checksums.
- Container atau systemd/Pterodactyl deployment strategy.
- Tutup ADR update mechanism sesuai deployment target yang dipilih.
- Migration dry-run, backup, restore, rollback commands.
- Operator runbook dan alerts.
- Hapus Python setelah seluruh tenant cutover.
- Hapus Node hanya jika ADR native Go disetujui; jika sidecar permanen, pin dan minimalisasi tanggung jawabnya.
- Deprecate protocol v2 `folderPath` identity setelah migration window.
- Remove legacy readers setelah retention period dan restore drill.

### Exit gate

- Fresh install dan upgrade install automated.
- Restore drill berhasil.
- Production readiness dashboard dan alerts aktif.
- Tidak ada undocumented runtime dependency.
- Legacy service removal memiliki explicit sign-off.

## Critical path

```text
M0 baseline
  -> M1 foundation
  -> M2 protocol
  -> M3 stores
  -> M5 agent
  -> M6 durability features
  -> M7 Python cutover
  -> M9 native gateway (optional based on M8)
  -> M10 cleanup
```

M4 control plane dapat berjalan paralel setelah M2/M3. M8 native spike dapat berjalan paralel, tetapi tidak boleh memblokir pemindahan agent ke Go.
