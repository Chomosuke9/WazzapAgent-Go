# Baseline Fitur

Dokumen ini menginventaris fitur project referensi. Implementasi baru tidak wajib menyalin wire shape, schema database, internal IDs, atau struktur process lama.

## Sumber referensi

| Area | Sumber |
|---|---|
| Produk dan operasi | `../wazzapagents/wazzapagent/README.md:7-203` |
| Event/action lama | `../wazzapagents/wazzapagent/src/account/actionDispatcher.ts:435-957` |
| Message normalization | `../wazzapagents/wazzapagent/src/wa/inbound.ts:264-609` |
| Account lifecycle | `../wazzapagents/wazzapagent/src/account/accountCatalog.ts:187-391` |
| Commands | `../wazzapagents/wazzapagent/src/wa/commands/` |
| Agent behavior | `../wazzapagents/wazzapagent/python/bridge/agent/batch_processor.py:276-1300` |
| Control panel | `../wazzapagents/wazzapagent/src/controlPanel/server.ts:594-1517` |
| Existing tests | `../wazzapagents/wazzapagent/tests/node`, `../wazzapagents/wazzapagent/python/tests` |

## Prioritas fitur

### P0 — Runtime inti

- Fresh QR/pairing-code login dan persistent native session.
- Reconnect, logout, device removal, graceful shutdown.
- Multi-account dengan stable `TenantID` dan isolated state.
- Incoming DM/group messages dan outgoing text.
- Structured logging, liveness, readiness, config validation.
- SQLite schema baru dan repositories baru.

### P1 — Message dan action inti

- Reply/quoted message.
- Mentions, tag-all detection, LID/phone JID mapping.
- Image, video, audio, document, dan sticker send/download.
- Reactions, delete message, mark read, presence.
- Group metadata, admin/superadmin roles, kick member.
- Per-chat serialization dan idempotent action handling.

### P2 — Agent

- Per-chat history dan context formatting.
- Debounce default 5 detik dan burst cap default 20 detik.
- Stale batch sebagai context-only.
- LLM1 routing: respond, react, atau sticker.
- LLM2 tools/actions, primary/fallback provider, retries, multimodal fallback.
- Permission, activation, mute gate, idle trigger, reply dedup.
- Durable action receipts dan history finalization.

### P3 — Product features

- Settings, prompt, mode, trigger, model, memories.
- Activation codes dan bot config.
- Slash commands dari 41 source files lama; daftar final ditentukan dari command registry baru.
- Sticker catalog dan conversion.
- Quiz, buttons, carousel, copy-code, Lottie, dan HTML rendering bila native library mendukung.
- Broadcast dan announcement.

### P4 — Background workflows

- One-shot scheduled tasks.
- Daily tasks dengan timezone eksplisit.
- Direct invoke API yang fail-closed.
- Sub-agent submit, progress, steering, resumable upload, webhook, durable completion, output delivery, retry/dead-letter.

### P5 — Control panel dan operasi

- Auth dan rate limiting.
- Account pairing/reconnect/logout.
- Settings, model, memories, activation, bot config, sticker management.
- Environment/config editor dengan secret masking.
- Audit log, health, metrics, backup, restore, dan release status.

## Canonical action model baru

Gunakan typed domain requests, bukan frame Node/Python lama:

- `SendMessage`
- `ReactMessage`
- `DeleteMessage`
- `KickMembers`
- `MarkRead`
- `SetPresence`
- `RunCommand`
- `GetChatContext`
- `SendSticker`
- `SendQuiz`
- `SendButtons`
- `SendCarousel`
- `SendCopyCode`
- `RenderHTML`
- `DownloadMedia`

Setiap durable action memiliki tenant-scoped idempotency key, normalized result, stable error code, dan explicit timeout. Fire-and-forget hanya dipakai bila duplicate/loss aman.

## Incoming message model baru

Minimal field:

- tenant/account identity;
- chat/message identity dan timestamp;
- private/group type dan chat name;
- sender JID/ref/name/roles;
- bot roles dan `fromMe`;
- normalized text/message type;
- quoted message;
- mentions/tag-all/replied-to-bot;
- attachments dan lazy materialization state;
- location;
- group event;
- command parsing result.

Model tidak perlu mempertahankan six-digit `contextMsgId`. Pilih stable opaque ID dengan retention dan lookup semantics yang jelas.

## Native WhatsApp capability matrix

Wajib diuji pada account baru:

- QR dan pairing number;
- session reconnect setelah process/host restart;
- private dan group text;
- quoted/reply, mention, tag-all, reaction, delete;
- image/video/audio/document/sticker;
- view-once, ephemeral, edited, dan protocol wrappers;
- LID/phone mapping dan participant roles;
- kick member dan group metadata updates;
- buttons, carousel, copy-code, quiz, Lottie;
- reconnect setelah network loss, conflict, stream replacement, dan logout;
- multiple accounts aktif bersamaan.

Fitur native yang tidak didukung harus memiliki fallback atau dikeluarkan dari scope release secara eksplisit.

## Agent semantics

### Batching

- Messages serialized per chat; chat berbeda concurrent.
- Debounce, burst limit, stale policy, dan cancellation memakai fake-clock tests.
- Prefix message dapat membatalkan LLM1 yang sedang berjalan bila mode mengizinkan.

### LLM1

- Primary/fallback provider.
- Typed tool decision dan schema validation.
- Endpoint kosong dapat memakai deterministic respond-default.
- Timeout, retry, history limit, dan message truncation configurable.

### LLM2

- Primary/fallback dan bounded retry/backoff.
- Typed tools dan result validator.
- Vision input dengan text-only fallback.
- Per-chat model override dan optional sub-agent tool.
- Prompt ordering ditetapkan oleh golden tests baru.

### Jobs dan sub-agent

- One-shot task tidak hilang saat shutdown.
- Daily task menyimpan timezone dan next-fire semantics eksplisit.
- Webhook completion diakui setelah durable storage.
- Output delivery memakai per-part checkpoints agar restart tidak menggandakan attachment.

## Data domain baru

Schema v1 dirancang dari kebutuhan, bukan menyalin file database lama:

- tenants/accounts and device metadata;
- chat settings and directory;
- models/provider config;
- activation and memories;
- moderation;
- stats;
- stickers/media metadata;
- scheduled/daily jobs;
- action receipts/inbox/outbox;
- sub-agent tasks/progress/outputs/delivery;
- audit events;
- `schema_migrations`.

## Control panel

Route baru boleh berbeda. Acceptance berdasarkan kemampuan:

- secure login/token setup;
- account lifecycle;
- tenant-scoped settings and resources;
- secret-safe config;
- system health and logs;
- sub-agent retry/dead-letter controls;
- backup/restore and version visibility.

## Baseline sign-off

Baseline selesai bila:

- semua fitur di atas berstatus `required`, `deferred`, atau `rejected`;
- setiap required feature memiliki acceptance criteria;
- canonical Go domain types dan error codes disetujui;
- native capability matrix memiliki test plan;
- tidak ada requirement import atau compatibility dengan state lama.
