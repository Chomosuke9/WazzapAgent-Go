# Baseline Fitur

Dokumen ini memetakan fitur project referensi ke Part rewrite. Implementasi baru mempertahankan perilaku yang dipilih, bukan struktur, protocol, storage, atau auth state lama.

## Sumber referensi

| Area | Lokasi lama |
|---|---|
| Startup dan account | `../wazzapagents/wazzapagent/src/index.ts`, `src/account` |
| WhatsApp inbound/outbound | `../wazzapagents/wazzapagent/src/wa` |
| Command | `../wazzapagents/wazzapagent/src/wa/command` |
| Control panel | `../wazzapagents/wazzapagent/src/controlPanel` |
| Agent/history/LLM | `../wazzapagents/wazzapagent/python/bridge` |
| Existing behavior tests | `../wazzapagents/wazzapagent/tests/node`, `python/tests` |

Status:

- `part-1`: wajib untuk barebone canary hari ini;
- `part-N`: direncanakan pada Part tersebut;
- `rejected`: sengaja tidak dipertahankan;
- `reference-only`: hanya sumber fixture/behavior comparison.

## Part 1 profile

Part 1 hanya membuktikan text vertical slice:

```text
WhatsApp text -> senderRef -> per-chat prompt -> text LLM -> WhatsApp text
```

Part 1 bukan stable v1.0 dan tidak menggantikan service lama. Label artifact/deployment adalah `v0.1-canary`.

## Runtime dan account

| Fitur | Part | Catatan |
|---|---:|---|
| Typed config, logging, health, graceful shutdown | 0/1 | Skeleton Part 0, production use Part 1. |
| Fresh QR atau pairing-code login | 1 | Minimal satu flow wajib lulus real device. |
| Persistent native session | 1 | Reconnect setelah process restart. |
| Network-loss reconnect hardening | 2 | Part 1 hanya bounded reconnect minimum. |
| Logout dan device removal operations | 5 | Kill switch Part 1 cukup stop/disable canary. |
| One active account surface | 1 | Dedicated test account. |
| Explicit `TenantID` everywhere | 1 | Wajib sejak schema/contract pertama. |
| Multiple active accounts | 5 | Bukan alasan memakai globals sekarang. |
| Control panel | 5 | Setelah application API stabil. |
| Node/Python sidecar | rejected | Single Go binary. |
| Import Baileys auth/data/config | rejected | Fresh state only. |

## Message dan identity

| Fitur | Part | Catatan |
|---|---:|---|
| Incoming/outgoing text | 1 | DM dan group mention yang allowlisted. |
| Ignore self/status/duplicate event | 1 | Single deterministic inbound pipeline. |
| Opaque stable senderRef | 1 | Per tenant/chat/LID, durable, collision-safe, resolvable dua arah. |
| Raw JID hidden from model/log | 1 | JID hanya di adapter/mapping store. |
| Persistent internal message ID | 1 | Dibutuhkan inbox/action causation. |
| Conversation history | 2 | Tidak masuk Part 1 model context. |
| Quoted-message lookup/reply | 2 | Stable mapping and hydration. |
| Replied-to-bot trigger | 2 | Part 1 group trigger hanya mention. |
| Batching/debounce/burst | 2 | Part 1 proses satu inbound turn. |
| LID identity + phone alias | core | LID canonical; phone hanya alias. Intake gagal tertutup bila LID tidak tersedia atau round-trip mapping tidak konsisten. |
| Command/AI handler isolation | 2 | Durable intake merutekan ke queue dan worker pool terpisah. |
| View-once/ephemeral/edited wrappers | 8 | Capability and privacy review required. |

## Prompt, agent, dan commands

| Fitur | Part | Catatan |
|---|---:|---|
| Chat-scoped `Agent` aggregate | 1 | One live object per tenant/account/chat key. |
| Lazy bounded `AgentRegistry` | 1 | Coalesced construction, pinning, idle eviction. |
| `Agent.Invoke()` façade | 1 | Text-only Part 1, idempotent durable turn plan and dispatcher internally. |
| Versioned `Agent.Config()` | 1 | Durable refresh, defensive snapshot, expected-version CAS mutation. |
| Base system prompt | 1 | Trusted message terpisah dari user text. |
| Per-chat prompt persistence | 1 | Stored within Agent Config; size bounded. |
| `/prompt view|set|clear` | 1 | Owner authorization outside Agent method. |
| Text-only OpenAI-compatible LLM | 1 | Single provider/model, non-streaming. |
| Plain text response | 1 | Output bounded dan validated. |
| `/help`, `/info`, `/reset` | 2 | Added after history/status contracts exist. |
| `Agent.History().List/Append/Reset/Trim` | 2 | Durable history and golden serialization. |
| LLM tools/typed actions | 3 | Model cannot run generic commands. |
| Permission roles and command registry | 3 | Part 1 has only fixed owner rule for `/prompt`. |
| Provider fallback/model selection | 3 | Part 1 intentionally one provider/model. |
| LLM1 router | rejected | Deterministic application trigger. |
| Auto/prefix/hybrid/idle routing | 3 or later | Only if measurement justifies them. |
| Durable memory | later | Separate from rolling history and prompt. |

## WhatsApp actions dan media

| Fitur | Part | Catatan |
|---|---:|---|
| `SendText` | 1 | Only model effect in Part 1. |
| Reaction/delete/read/presence/chat context | 3 | Typed and permission checked. |
| Group admin/kick/moderation | 8 | Destructive capability gate. |
| Image receive/send/vision | 4 | Lazy materialization and strict limits. |
| Document/audio/video | 4+ | One capability at a time. |
| Sticker/buttons/carousel/quiz/copy-code | 8 | Capability-aware fallbacks. |
| Lottie/HTML rendering | 8 | Security review mandatory. |
| Generic model-driven `run_command` | rejected | Model gets explicit typed capabilities only. |

## Background workflows dan operations

| Fitur | Part | Catatan |
|---|---:|---|
| Inbox/outbound intent/receipt minimum | 1 | Prevent silent drop and unsafe replay. |
| Full history/outbox reconciliation/retention | 2 | Includes crash/network fault matrix. |
| Multi-account resource budgets | 5 | Per-tenant queue/semaphore/storage. |
| Scheduler/daily task | 6 | Transactional lease and readiness gate. |
| Direct invoke | 6 | Authenticated limited principal. |
| Sub-agent | 7 | Durable job and content-addressed files. |
| Advanced WhatsApp | 8 | Per-capability tests. |
| Stable release hardening | 9 | Soak, scale, restore, artifacts, runbooks. |
| Writable-Git self update | rejected | Deploy reproducible artifacts. |

## Canonical Part 1 models

### Inbound message

Minimum fields:

- `TenantID`, `ChatID`, `MessageID`, `ParticipantID`;
- provider dedup identity stored only in adapter/store mapping;
- private/group kind;
- normalized text;
- `MentionsBot` for group trigger;
- sender display name and `SenderRef`;
- verified `FromMe` and configured-owner match;
- occurred/received timestamps;
- structured provenance.

No raw protobuf, JID, provider DTO, local path, media bytes, quoted content, or role claimed by the model.

### Agent

- key: `(TenantID, AccountID, ChatID)`;
- obtained through lazy `AgentRegistry.AgentFor(...)`;
- exposes `Key`, `Config`, and `Invoke` in Part 1;
- adds `History` child capability in Part 2;
- serializes invocation for its own chat while different Agents can run concurrently;
- claims `(InvocationID, digest)`, reuses any stored response plan, and captures one refreshed immutable Config version only for new generation;
- atomically stores response/action plan before calling a durable `ResponseDispatcher`; never calls Hypermeow directly;
- performs domain validation but no actor authorization.

Config stores credential-free `Model`, configurable base `Prompt`, optional durable per-chat `PromptOverride`, and immutable `Permission` policy reference/revision. `/prompt set|clear` changes only the override. External policy/application handlers refresh and authorize a snapshot before calling actor-free Config methods. A non-sensitive `ConfigChanged` notification is attempted only after durable compare-and-swap succeeds; notification loss never fails the committed mutation.

### Text action

Part 1 has one action type:

```text
SendText {
  ActionID, TenantID, ChatID,
  CausationRef, CorrelationID,
  IdempotencyKey, Deadline, Text
}
```

The application creates IDs and idempotency key. The model returns text only and cannot choose tenant, chat, authority, or key.

### Sender ref

- scope: `(tenant_id, account_id, chat_id, LID)`; `participant_id` hanya surrogate internal;
- unique display ref inside `(tenant_id, chat_id)`;
- random and opaque rather than derived from LID/phone JID;
- mapping `senderRef ⇄ LID` wajib unik dan diverifikasi dua arah dalam transaksi intake;
- persisted before model invocation;
- stable after process restart/backup restore;
- never used as authorization evidence.

## Part 1 acceptance criteria

### Runtime

- Invalid config fails before network startup.
- Live/readiness and account readiness are distinct.
- SIGTERM cancels workers and closes stores within timeout.
- Dedicated account can pair and reconnect after process restart.

### Message and senderRef

- Eligible allowlisted DM receives one text reply.
- Allowlisted group mention receives one text reply.
- Self/status/duplicate/non-trigger group events produce no reply.
- Same participant keeps the same ref after restart.
- LID yang sama tetap mendapat senderRef yang sama walaupun alias nomor/pushName berubah.
- Event tanpa LID atau mapping ambigu gagal tertutup dan tidak boleh membuat identity baru dari nomor.
- Command tetap berjalan ketika worker AI diblokir.
- Different tenant/chat scopes do not accidentally share mapping.
- Model/log payload contains no raw JID/phone number.

### Prompt

- Verified configured owner can set/view/clear prompt.
- Non-owner receives deterministic denial and never reaches LLM.
- Oversized/invalid prompt is rejected without mutation.
- Prompt survives restart and is isolated per chat/tenant.

### Durability and concurrency

- Duplicate provider event produces at most one planned response.
- Same idempotency key/fingerprint cannot send twice.
- Same key with different fingerprint fails as conflict.
- Ambiguous send outcome becomes `unknown_outcome` and is not auto-replayed.
- Same chat serializes while different chats can progress concurrently.
- Queue, LLM calls, timeouts, and response sizes are bounded.

### Canary

- Test account, data root, process/port, logs, and allowlist are isolated from the old service.
- Normal startup enables the Agent only after a narrow dedicated-account allowlist is configured; an optional disable override supports staged pairing.
- Kill switch and rollback are tested.
- Deployment is reported as canary only.

## Promotion rule

A feature moves earlier only when:

1. Part 1 cannot work safely without it;
2. its owner and interface are clear;
3. it has acceptance and rollback tests;
4. it does not expand the model's authority;
5. it does not delay the barebone canary for convenience or polish.
