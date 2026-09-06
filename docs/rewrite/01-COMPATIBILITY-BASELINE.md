# Baseline Kompatibilitas

Dokumen ini menjadi checklist parity. Actual runtime, TypeScript types, Python SDK, tests, dan docs harus dibandingkan; `CONTRACT.md` tidak boleh dipakai sendiri karena sudah mengalami drift.

## Sumber baseline

| Area | Sumber utama |
|---|---|
| Protocol docs | `../wazzapagents/wazzapagent/CONTRACT.md:1-312` |
| TypeScript frames | `../wazzapagents/wazzapagent/src/protocol/types.ts:14-201` |
| Runtime action map | `../wazzapagents/wazzapagent/src/account/actionDispatcher.ts:941-957` |
| Python SDK | `../wazzapagents/wazzapagent/python/wasocket/socket.py:277-294`, `:541-637` |
| Incoming normalization | `../wazzapagents/wazzapagent/src/wa/inbound.ts:264-609` |
| Python behavior | `../wazzapagents/wazzapagent/python/bridge/agent/batch_processor.py:276-1300` |
| HTTP API | `../wazzapagents/wazzapagent/src/controlPanel/server.ts:594-1517` |
| Database | `../wazzapagents/wazzapagent/src/db/schema/index.ts:80-425` |
| Existing tests | `../wazzapagents/wazzapagent/tests/node`, `../wazzapagents/wazzapagent/python/tests` |

## Protocol

Protocol handshake memakai versi `2.0`. `hello` membawa `folderPath`, versi, dan optional tenant token. `hello_ack` mengembalikan normalized WhatsApp status. Ready WS tidak berarti WhatsApp siap menerima action.

Referensi: `../wazzapagents/wazzapagent/CONTRACT.md:37-70`.

### Action aktual

| Action | ACK | Catatan |
|---|---|---|
| `send_message` | `action_ack` dan `send_ack` saat sukses | text/media/reply |
| `react_message` | `action_ack` | context ID lookup |
| `delete_message` | `action_ack` | context ID lookup |
| `kick_member` | `action_ack` dan error saat gagal | partial/all-or-nothing |
| `mark_read` | tidak ada | fire-and-forget |
| `send_presence` | tidak ada | fire-and-forget |
| `run_command` | `action_ack` | silent command invocation |
| `get_chat_context` | `action_ack` | authoritative cold context |
| `relay_lottie_sticker` | `action_ack` | raw Lottie payload |
| `send_quiz` | `action_ack` | 2–5 choices |
| `send_buttons` | `action_ack` | native flow |
| `send_carousel` | `action_ack` | native flow cards |
| `send_copy_code` | `action_ack` | native CTA |
| `render_html` | `action_ack` | runtime extension |
| `download_media` | `action_ack` | lazy media materialization |

Runtime map: `../wazzapagents/wazzapagent/src/account/actionDispatcher.ts:941-957`.

### Event dan control frame

- `incoming_message`: best effort.
- `whatsapp_status`: reliable.
- `action_ack`, `send_ack`, `error`: runtime delivery perlu diverifikasi, bukan hanya mengikuti docs.
- Control frames memakai field top-level, bukan `payload`.
- Control events: `clear_history`, model invalidation/set, settings invalidation, subagent enable, scheduled/daily tasks, dan mute.

Referensi: `../wazzapagents/wazzapagent/src/protocol/types.ts:180-201`.

### Drift wajib diselesaikan di Phase 0

1. `render_html` dan `download_media` belum lengkap di `CONTRACT.md`.
2. `download_media` tidak ada dalam `InboundActionFrame` TypeScript, tetapi ada di runtime dan Python SDK.
3. `Attachment.path` typed sebagai wajib `string`, sementara lazy incoming attachment dapat memakai `path: null` dan `pending: true`.
4. Docs menyebut actions/ACK best effort, tetapi Python dan Node memakai reliable queues pada jalur aktual tertentu.
5. Beberapa action result mengembalikan raw Baileys object. Bentuk ini tidak portable ke adapter native Go.
6. Protocol type tests belum mencakup seluruh runtime action.

Deliverable: satu JSON Schema/OpenAPI-compatible schema yang menghasilkan Go/TS/Python fixtures dan contract tests.

## Incoming message behavior

Payload parity minimal mencakup:

- tenant/instance/chat/message identity;
- `contextMsgId` enam digit dan sender reference;
- sender/bot roles, owner, group/private state;
- `fromMe`, `contextOnly`, `triggerLlm1`, timestamp, message type;
- text, quoted message, mentions, tag-all, replied-to-bot;
- attachments termasuk lazy media state;
- location, group description, slash command, group event, action log.

Referensi bentuk: `../wazzapagents/wazzapagent/src/protocol/types.ts:115-178`.

Golden fixtures harus mencakup wrapper protobuf, edited/view-once/ephemeral messages, LID dan phone JID, quoted media, mention binding, reactions, group events, bot messages, serta stale messages.

## Gateway dan account behavior

- Catalog priority: explicit account JSON, managed `accounts.json`, `FOLDER_PATHS`, lalu single-account fallback.
- Account identity sekarang normalized absolute `folderPath`.
- Setiap account memiliki socket, DB/repositories, caches, send queue, reliable queue, media, sticker, dan auth state sendiri.
- Account removal menghentikan runtime tetapi mempertahankan data.
- Tombstone mencegah late reconnect setelah removal.
- Pairing code memiliki cooldown; QR hanya dicetak terbatas agar log tidak banjir.
- Cold work menunggu WhatsApp status `open`.

Referensi: `../wazzapagents/wazzapagent/src/account/accountCatalog.ts:187-391`, `../wazzapagents/wazzapagent/src/server/accountRegistry.ts:37-260`, `../wazzapagents/wazzapagent/README.md:83-104`, `:190-203`.

## Command dan domain feature inventory

Port seluruh 41 source file di `../wazzapagents/wazzapagent/src/wa/commands/`, tetapi hitung command runtime dari registry; `index.ts` dan helper bukan selalu user-facing command.

Kategori parity:

- chat/default settings, trigger, mode, prompt, model;
- activation dan bot config;
- memory dan mention binding;
- moderation/mute/kick/delete;
- sticker add/send/list/remove dan media conversion;
- broadcast/announcement;
- scheduled dan daily tasks;
- help/context/debug-compatible output;
- buttons, quiz, carousel, copy-code, HTML rendering;
- permissions owner/admin/superadmin/user;
- compatibility/device gating.

## Agent behavior

### Batching

- Debounce default 5 detik, burst cap 20 detik.
- Batch lebih tua dari default 45 detik menjadi context-only.
- Per-chat serialization; chat berbeda tetap concurrent.
- Hybrid prefix interrupt membatalkan LLM1 dan menggabungkan message baru.

### LLM1

- Primary/fallback chain.
- Tool decision: respond, react, atau sticker.
- Endpoint kosong default merespons dengan confidence 50.
- Timeout/retry/history truncation/message truncation harus sama.

### LLM2

- Primary/fallback dan configurable retry/backoff.
- Vision attachments dengan fallback text-only.
- Output harus menghasilkan minimal satu action valid.
- Per-chat model override dan optional sub-agent tool.
- Urutan prompt harus golden-tested.

### History dan ACK hydration

- Format message IDs, timestamp, sender refs, roles, quoted hydration, dan injection guard harus parity.
- Provisional outgoing history diubah ke real `contextMsgId` dari ACK.
- Sub-agent attachments dipetakan 1:1 dengan ACK.

### Scheduler dan direct invoke

- One-shot task dihapus setelah success/hard failure, tetapi dipertahankan saat shutdown cancellation.
- Daily task tetap berulang dan memakai configured context timezone.
- Direct invoke fail-closed tanpa API key, memakai constant-time comparison, mengembalikan 202 sebelum background execution.

### Sub-agent

- Submit, retry, progress keepalive, steering, webhook auth, resumable upload, output SHA validation.
- Durable completion ownership, deferred callbacks, delivery checkpoints, recovery, tombstones, dan output spooling.
- Completion tidak boleh hilang atau dikirim dua kali setelah restart.

Referensi: `../wazzapagents/wazzapagent/python/bridge/session.py:147-722`, `../wazzapagents/wazzapagent/python/bridge/agent/subagent_coordinator.py:776-1300`, `../wazzapagents/wazzapagent/python/bridge/subagent/tracker.py:35-700`.

## Data inventory

Current tenant layout:

```text
<folderPath>/
  auth/
  db/
    settings.db
    stats.db
    moderation.db
    subagent.db
    stickers.db
    action-receipts.json
    subagent_tracker.json
  media/
  stickers/
```

Core tables:

- settings: `chat_settings`, `llm_models`, `chat_directory`, `owner_contact`, `bot_config`, `llm_provider_config`, `activation_codes`, `chat_activations`, `memories`, `memory_mentions`, `participant_names`, scheduled/daily tables;
- stats: `chat_stats`, `chat_user_stats`;
- moderation: `chat_mutes`;
- legacy sub-agent state and sticker catalog.

Schema source: `../wazzapagents/wazzapagent/src/db/schema/index.ts:80-425`, Python additions: `../wazzapagents/wazzapagent/python/bridge/db/core.py:548-806`.

Node dan Python saat ini sama-sama membuat/mengubah sebagian schema. Target wajib memiliki satu migration owner.

## Control panel API

Black-box snapshot seluruh route dan JSON shape dari `../wazzapagents/wazzapagent/src/controlPanel/server.ts:594-1517`:

- auth status dan overview;
- account create/update/remove/pair/reconnect/session reset;
- chat settings dan details;
- memories;
- LLM provider config dan model catalog;
- activation codes dan activations;
- bot config;
- sticker catalog/upload/delete;
- environment config;
- update/restart status/actions;
- sub-agent outbox admin;
- audit logs.

Security parity:

- API fail-closed saat token kosong;
- timing-safe token comparison;
- failed-auth rate limiting;
- security headers dan body limit;
- masked secrets;
- mutation audit trail;
- path/account ownership validation.

## Baseline sign-off

Phase 0 selesai hanya bila:

- inventory machine-readable memuat semua frame/action/event;
- generated fixtures lulus terhadap TS, Python, dan Go decoder;
- HTTP snapshots tersedia;
- schema snapshots semua tenant DB tersedia;
- golden prompt/action fixtures tersedia;
- behavior yang tidak dipertahankan memiliki ADR dan migration note.
