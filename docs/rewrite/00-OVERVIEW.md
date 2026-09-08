# Rencana WazzapAgent Go

## Tujuan

Membangun WazzapAgent baru di repository ini dengan Go. Project `../wazzapagents/wazzapagent` hanya menjadi referensi fitur dan perilaku produk; data, database, konfigurasi, auth state, serta deployment lama tidak dimigrasikan.

Referensi produk lama:

- gateway Node.js/Baileys untuk WhatsApp, command, media, dan control panel;
- bridge Python untuk batching, LLM1/LLM2, history, scheduler, direct invoke, dan sub-agent.

Referensi: `../wazzapagents/wazzapagent/README.md:7-12`.

## Strategi

1. Part 0 mengunci scope, ownership, kontrak, risiko, dan rencana.
2. Setiap Part berikutnya adalah vertical slice yang deployable, bukan layer teknis.
3. Part 1 menghasilkan barebone canary melalui chat-scoped `Agent`: text reply, versioned Agent Config/prompt, dan durable `senderRef`.
4. Reliability, commands/actions, media, multi-account, automation, sub-agent, dan advanced WhatsApp ditambahkan pada Part terpisah.
5. Setiap capability masuk melalui port kecil dan adapter; core Part sebelumnya tidak dibongkar.
6. Uji dengan account/data baru dan selalu bedakan local verification, production canary, serta stable release.

Tidak ada compatibility requirement terhadap protocol Node/Python, bentuk database lama, raw Baileys response, path tenant lama, atau auth state lama. Bentuk lama boleh dipakai sebagai fixture pembanding perilaku, bukan kontrak runtime.

## Sasaran akhir

- Satu service Go untuk WhatsApp, config, account lifecycle, agent, LLM, jobs, persistence, control panel, dan observability.
- Native `hypermeow` adapter di belakang consumer-owned narrow interfaces.
- Lazy bounded `AgentRegistry` dan satu Agent object per active tenant/account/chat, dengan process-local ordering di bawah account ownership lease.
- Authorization actor di application/policy layer; Agent core menjaga domain consistency tanpa memutuskan permission.
- Durable InvocationID/digest claim dan atomic response/action plan sehingga replay setelah planning tidak memanggil model atau membuat action baru.
- Stable `TenantID` dan layout data baru.
- Versioned schema sejak versi pertama.
- Transactional inbox/outbox, action receipts, scheduler claims, dan sub-agent state.
- Reproducible build, health/readiness, backup/restore, dan release runbook.

## Scope Part 1 — `v0.1-canary`

### Termasuk

- Satu active test account dengan internal `TenantID` dan tenant-scoped dependencies.
- Fresh pairing, persistent native session, reconnect setelah process restart, dan graceful shutdown.
- Incoming/outgoing text: eligible DM dan allowlisted group mention.
- Durable opaque `senderRef` per tenant/chat/participant; raw JID tidak dikirim ke model.
- Configurable base prompt dan per-chat append-only `PromptOverride` melalui `/prompt view|set|clear`, hanya untuk configured verified owner; safety/system policy tidak dapat ditimpa.
- Satu OpenAI-compatible text-only LLM, non-streaming, tanpa tools/history/media.
- Minimum durable inbox, outbound text intent, dan action receipt.
- Health, redacted logs, bounded queue/concurrency, timeout, kill switch, dan rollback.
- Dedicated account, data root, port, process, logs, dan recipient/chat allowlist.

### Ditunda ke Part berikutnya

- History, batching/debounce, quoted reply, replied-to-bot, dan commands selain `/prompt`.
- Model tools dan reaction/delete/read/presence/kick actions.
- Image, video, audio, document, sticker, dan interactive messages.
- Multiple active accounts dan control panel.
- Scheduler, direct invoke, sub-agent, activation, dan durable memory.
- Stable multi-platform release; Part 1 hanya canary terbatas.

### Tidak termasuk

- Import SQLite, JSON state, media, sticker, config, atau audit lama.
- Import auth Baileys; semua account melakukan pairing baru.
- Menjalankan Node atau Python sebagai sidecar.
- Menjaga protocol v2 atau control panel API lama secara byte-for-byte.
- Writable-Git self-update sebagai core feature.
- Menghentikan atau mengganti runtime lama selama Part 1 canary.

## Dokumen

1. [Master plan](../../PLAN.md)
2. [Baseline fitur](01-FEATURE-BASELINE.md)
3. [Arsitektur target](02-TARGET-ARCHITECTURE.md)
4. [Roadmap dan work breakdown](03-ROADMAP.md)
5. [Kontrak Agent-centric](04-AGENT-CONTRACT.md)
6. [Pengujian dan release](05-TESTING-RELEASE.md)
7. [Risiko dan keputusan](06-RISKS-DECISIONS.md)

## Definition of Done Part 1

Barebone canary dianggap tercapai ketika:

- fake end-to-end, unit, integration, race, vulnerability, dan build gates lulus;
- dedicated account dapat pair, reconnect, menerima, dan mengirim text;
- `senderRef` dan per-chat prompt survive restart serta tetap tenant/chat-scoped;
- duplicate/replay tidak membuat planned response/action ganda;
- allowlist, kill switch, backup awal, dan rollback probe bekerja;
- service lama tidak diubah dan tidak berbagi account/path dengan canary;
- hasil dilabeli `v0.1-canary`, bukan stable production release.

Stable release criteria berada di Part 9 pada roadmap.
