# Roadmap dan Work Breakdown

[`PLAN.md`](../../PLAN.md) adalah master plan. Dokumen ini merangkum delivery order dan exit gate setiap Part.

## Cara membaca roadmap

- Part adalah vertical slice yang deployable, bukan sekumpulan layer teknis.
- Part 0 berisi persiapan dan keputusan.
- Part 1 adalah barebone canary yang ditargetkan selesai hari ini.
- `v0.1-canary` bukan stable production release.
- Scope baru tidak boleh merusak isolation, durability, atau test guarantee Part sebelumnya.

## Part 0 — Persiapan dan rencana

### Tujuan

Mengunci masalah, scope, architecture boundary, risiko, dan urutan delivery sebelum coding fitur besar.

### Work items

- Inventaris fitur dan failure mode project lama.
- Tetapkan greenfield rewrite, fresh pairing, dan no Node/Python sidecar.
- Tetapkan modular monolith dan consumer-owned narrow interfaces.
- Tetapkan normative chat-scoped Agent/Registry/Config/History contract dan external authorization boundary.
- Tetapkan explicit `TenantID`, single ownership, bounded concurrency, dan durable correctness state.
- Tetapkan package layout bertahap dan extraction seams.
- Siapkan executable/config/logger/health/lifecycle/CI skeleton.
- Spike modernc/Hypermeow device store; hasil spike bukan production adapter.
- Tetapkan scope, schema minimum, tests, canary gate, kill switch, dan rollback Part 1.

### Exit gate

- Master plan dan Part 1 scope disetujui.
- Ownership/import rules tertulis.
- Scope dan non-scope Part 1 tidak ambigu.
- Foundation lokal lulus format, vet, test, race, dan build sebelum feature implementation dimulai.
- Tidak ada claim real-device/deployment tanpa bukti current run.

### Status 2026-09-08

- Planning/audit: selesai.
- Foundation dan Windows SQLite/Hypermeow spike: tersedia pada working tree.
- Part-based roadmap: diperbarui.
- Agent-centric contract: diperbarui.
- Current foundation validation: format, vet, test, race, build, and module verification pass.
- Part 1 `govulncheck` reachable-symbol scan lulus pada 2026-09-08.
- Real-device verification dan canary deployment: belum menjadi hasil perubahan dokumentasi ini.

## Part 1 — Barebone production canary

### Tujuan

Membuktikan vertical slice minimum pada dedicated test account:

```text
pair/connect
  -> text inbound
  -> dedup claim
  -> senderRef
  -> per-chat prompt
  -> text-only LLM
  -> durable SendText intent
  -> WhatsApp text outbound
  -> receipt completion
```

### Work stream A — Runtime dan native text

- Satu active account, tetapi semua contract tetap tenant-scoped.
- Fresh QR atau pairing-code flow; minimal satu lulus real-device test.
- Persistent native session dan reconnect setelah process restart.
- Incoming/outgoing text.
- Semua eligible DM; group hanya ketika bot di-mention.
- Ignore self/status/duplicate/unsupported event secara deterministik.
- Process readiness dan account readiness terpisah.

### Work stream B — Identity dan senderRef

- Canonical tenant/chat/message/participant identity.
- Provider JID/ID hanya di adapter mapping.
- Random, opaque, human-readable sender ref per tenant/chat/participant.
- Mapping durable dengan unique constraints dan collision retry.
- Ref stabil setelah restart dan restore.
- Sender ref tidak pernah menjadi role/permission proof.

### Work stream C — Prompt

- Base system prompt.
- Versioned per-chat `Agent.Config` snapshot with credential-free model, configurable base prompt, per-chat prompt override, and immutable permission policy reference/revision.
- Exact `/prompt view`, `/prompt set <text>`, dan `/prompt clear` parsing.
- `/prompt set|clear` changes only `PromptOverride`, never base or non-overridable safety instructions.
- External application/policy handler refreshes Config and verifies the configured owner before calling actor-free Agent Config methods.
- Config mutation binds external authorization to an expected snapshot version, then uses durable CAS, atomic snapshot swap, and best-effort post-commit `ConfigChanged` notification.
- Prompt command tidak diteruskan ke model.
- Authenticated opaque senderRef, untrusted display name, configured prompt, dan raw user text tidak digabung menjadi satu untyped transcript.

### Work stream D — Barebone agent

- Lazy `AgentRegistry`, one live Agent per tenant/account/chat key.
- `Agent.Invoke()` as the chat-scoped façade.
- Agent owns invocation serialization, durable `(InvocationID, digest)` claim, and stored-plan replay.
- External invocation policy decision is bound to the refreshed Config version; stale decisions conflict before generation.
- New generation captures one refreshed immutable Config version; replay after planning skips the model.
- OpenAI-compatible text-only provider.
- Non-streaming, single model, timeout, bounded safe retry.
- Plain text output dengan hard size limit.
- Durable `ResponseDispatcher`; Agent never imports Hypermeow directly.
- No history, batching, tools, generic commands, media, or fallback chain.
- Fake deterministic LLM untuk tests.

### Work stream E — Minimum durable delivery

- Embedded immutable migrations.
- Tenant/chat/address, participant/sender ref, prompt, inbox, outbound action, dan receipt state.
- Provider inbound key unik.
- TurnStore binds unique `(AgentKey, InvocationID)` to a canonical digest and bounded generation lease.
- Atomically store response text, response/action IDs, outbound intent, and planned state before network send.
- Replay after planning skips ModelInvoker and reuses the same action.
- Unknown send outcome tidak diulang otomatis.
- Fake clock/store/sender untuk replay dan crash-boundary tests.

### Concurrency

- Bounded inbound queue.
- Per-Agent invocation gate: same Agent serial, different Agents concurrent.
- Registry coalesces concurrent construction, pins in-flight Agents, enforces a hard bound, and idle-evicts safely.
- Global LLM semaphore.
- Per-chat/JID outbound ordering.
- Semua goroutine mengikuti root context dan `WaitGroup`.

### Implementation order hari ini

1. Freeze `04-AGENT-CONTRACT.md`: Agent key, Registry, Config, generic causation, TurnStore replay, ResponseDispatcher, sender ref, errors, and narrow ports.
2. Implement minimum migrations and transactional store use cases, including versioned `agent_configs` CAS and atomic response/action planning.
3. Make fake source -> AgentRegistry -> Config.Refresh/external policy -> Agent.Invoke -> TurnStore -> fake model -> durable dispatcher -> fake sender pass.
4. Build internal Hypermeow pair/connect/text source/text sender adapter.
5. Build text-only OpenAI-compatible adapter.
6. Add recovery, replay, concurrency, bounds, cancellation, and redaction tests.
7. Add fail-closed allowlist, generated durable runtime identity, safe normal defaults, readiness, optional kill switch, backup, and rollback probe.

This is the cut line. No later-Part feature may enter before all seven steps pass.

### Explicit non-scope

- history, batching/debounce, contextMsgId, quoted reply, replied-to-bot;
- commands selain `/prompt`;
- reaction/delete/read/presence/kick/tool calling;
- image dan media lain;
- multi-account product surface, control panel, scheduler, direct invoke, sub-agent;
- replacement atau shutdown service lama.

### Exit gate lokal

- `gofmt`, `go vet`, `go test`, `go test -race`, build, dan `govulncheck` lulus.
- Fake end-to-end test lulus.
- Sender-ref/prompt tenant isolation tests lulus.
- Duplicate inbound/action, timeout, cancellation, and same-chat ordering tests lulus.
- Secret, raw JID, prompt content, dan message body tidak muncul di normal logs.

### Exit gate real-device

- Dedicated account dapat pair dan reconnect setelah restart.
- Allowlisted DM response lulus.
- Allowlisted group mention response lulus.
- Prompt set/view/clear dan restart persistence lulus.
- Replay probe tidak menghasilkan duplicate planned response.
- Kill switch dan rollback dicoba.

### Canary constraints

- Dedicated WhatsApp test account.
- Dedicated data root, port, service/process, dan logs.
- Recipient/chat allowlist wajib dan fail-closed.
- Response agent disabled by default selama pair/reopen/backup dan diaktifkan hanya untuk bounded send probe.
- Service lama tetap berjalan dan tidak dimodifikasi.
- Backup awal kedua database setelah pairing.
- Label deployment `v0.1-canary`, bukan stable release.

### Status implementasi 2026-09-08

- Work stream A-E dan concurrency/hardening tersedia pada working tree.
- Runbook operasional tersedia di `07-PART1-RUNBOOK.md`.
- Local format/module/vet/test/race gates, four-target `CGO_ENABLED=0` cross-build, vulnerability scan, dan disabled-runtime HTTP process smoke lulus pada Go 1.27.0; test/vet juga lulus pada minimum Go 1.26.5.
- Real-device dan production-host gates tetap pending sampai dedicated canary resources tersedia.

## Part 2 — Reliable conversation core

### Tujuan

Membuat percakapan text tahan restart dan kaya konteks tanpa membuka tool berbahaya.

### Work items

- Persistent full canonical transcript untuk chat allowlisted, dengan bounded model context dan retention.
- Passive group text/sticker tetap dicatat sebagai context tetapi tidak memicu respons; trigger policy tetap berada di inbound layer.
- Implement `Agent.History().List/Append/Reset/Trim` as the Part 2 child capability; externally authorized read/reset receives and transactionally verifies the authorized Config version.
- Stable internal message ID dan quoted lookup.
- Replied-to-bot trigger.
- Debounce, burst cap, stale context, dan reply dedup.
- Deterministic context builder dan golden serialization.
- Structured provenance dan context injection defense.
- `/help`, `/info`, `/reset`.
- LID canonical identity dengan durable `senderRef ⇄ LID` round-trip invariant; phone JID hanya alias.
- Command dan AI handler memakai queue/worker pool terpisah agar model stall tidak membekukan command.
- Backup/restore, action reconciliation, and richer metrics.

### Exit gate

- History/context survive restart.
- Allowlisted passive group entries survive restart and appear in the next eligible bounded context without triggering a standalone response.
- Same-chat ordering dan cross-chat concurrency lulus under load.
- Network-loss/process-kill/replay tests tidak membuat silent loss atau duplicate visible response.

### Status implementasi 2026-09-10

- Full durable transcript child, version-guarded reset/read, retention, canonical quote, replied-to-bot, bounded context builder, provenance boundary, batching/debounce/burst cap, dan control commands telah diimplementasikan. Passive group message tersimpan tetapi tidak memicu invocation.
- Action/inbound recovery tetap memakai durable lease/outbox; expired executing send menjadi `unknown_outcome` dan tidak di-resend secara buta.
- Offline full-data backup, checksum verification, restore-to-new-directory, conversation metrics, dan local fault/replay tests tersedia.
- LID-first sender mapping dan isolated command/AI lanes tersedia; local round-trip serta blocked-AI isolation tests lulus.
- Real-device DM/group reply, reconnect, forced process kill, network-loss observation, restore drill, dan soak masih pending; Part 2 belum boleh disebut production-complete atau stable.

## Part 3 — Permission, commands, dan typed actions

### Work items

- Human/model/system/recovery principals.
- Current role/capability resolution.
- Permission recheck immediately before side effect.
- Keep actor verification and authorization in application/policy/action layers, never inside Agent core methods.
- Explicit command registry.
- Typed actions untuk react, delete, mark-read, presence, dan chat context.
- Durable receipt state machine and unknown-outcome reconciliation.
- Provider fallback dan bounded retry policy.
- Tolak generic model-generated `run_command`.

### Exit gate

- Model tidak dapat memperoleh owner/admin authority.
- Setiap effect melewati validate -> authorize -> claim -> execute -> finalize.
- Permission, replay, conflict, and unknown-outcome tests lulus.

## Part 4 — Media dan rich context

### Work items

- Image receive/send dan lazy materialization.
- Vision input serta text-only fallback.
- MIME/size/pixel/hash/timeout/quota/cleanup checks.
- Mention/reply rendering lanjutan.
- Tambahkan document/audio/video satu per satu setelah capability gate.

### Exit gate

- Raw path/protobuf/provider DTO tidak keluar dari adapter.
- SSRF, traversal, decompression, and oversized-media tests lulus.
- Real-device image matrix lulus pada target yang didukung.

## Part 5 — Multi-account dan control plane

### Work items

- Multiple active account runtimes dengan per-tenant budgets.
- Account ownership registry dan state machine.
- Two-tenant real runtime isolation.
- Versioned control API dan embedded UI.
- Server-side session, CSRF, rate limiting, and audit.
- Account, prompt/model, backup/restore operations.

### Exit gate

- Zero cross-tenant path/data/config/secret access.
- Lifecycle races, noisy-neighbor, auth, and browser/API tests lulus.

## Part 6 — Scheduler dan direct invoke

### Work items

- Shared cold invocation path.
- One-shot dan daily task dengan IANA timezone.
- Transactional leases and account-readiness gate.
- Authenticated direct invoke.
- Limited scheduler/direct principal; never fake owner.
- Use the existing typed `CausationRef` (`task` or `request`) rather than overloading a WhatsApp MessageID.

### Exit gate

- Restart, overdue, timezone, lease expiry, and cancellation tests lulus.
- Cold invoke refreshes live chat context.

## Part 7 — Sub-agent

### Work items

- Durable job state machine.
- Authenticated submit/callback.
- Content-addressed input/output with size/hash verification.
- Progress, steering, correction, cancellation, retry/dead-letter.
- Output delivery through normal action outbox.

### Exit gate

- Crash at every transition preserves completion and avoids duplicate file delivery.
- Waiting never holds a chat lock.

## Part 8 — Advanced WhatsApp features

- Sticker, buttons, carousel, quiz, copy-code, Lottie, and safe HTML.
- Moderation/kick/broadcast/announcement.
- View-once, ephemeral, edited, and uncommon wrappers.
- Capability-aware fallback per client/platform.

### Exit gate

- Each feature passes adapter capability, security, and real-client compatibility gates.
- No domain dependency on raw protobuf workaround.

## Part 9 — Scale, hardening, dan stable release

### Work items

- Security and dependency/license audit.
- Load, fault injection, backup/restore, migration, and 24+ hour soak.
- Per-tenant resource budgets and noisy-neighbor tests.
- Tenant sharding/lease only if measurements require it.
- Debian, Windows, and Termux artifacts/checksums/runbooks.
- Stable release sign-off.

### Exit gate

- Zero tenant-isolation violation.
- No unbounded queue/goroutine/WAL/file/memory growth.
- Recovery and rollback drills pass.
- Runtime requires no Node/Python.

## Critical path

```text
P0 plan
 -> P1 barebone canary
 -> P2 reliable conversation
 -> P3 safe actions
 -> P4 media/context
 -> P5 multi-account/control
 -> P6 automation
 -> P7 sub-agent
 -> P8 advanced WhatsApp
 -> P9 stable release
```

## Stop conditions

Stop the current Part or rollout when:

- tenant isolation fails;
- canary account/path overlaps the old service;
- a secret or raw identifier leaks;
- a duplicate or ambiguous outbound action is replayed unsafely;
- a queue/resource grows without bound;
- permission trusts model-provided roles;
- DB integrity/migration verification fails;
- kill switch or rollback is not usable.
