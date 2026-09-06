# Rencana Rewrite WazzapAgent ke Go

## Tujuan

Memindahkan project sumber di `../wazzapagents/wazzapagent` ke repository ini dengan Go, sambil menjaga perilaku pengguna, isolasi tenant, data SQLite, dan kompatibilitas integrasi selama migrasi.

Project lama terdiri dari dua runtime:

- gateway Node.js/Baileys untuk WhatsApp, WebSocket, command, media, dan control panel;
- bridge Python untuk batching, LLM1/LLM2, history, scheduler, direct invoke, dan sub-agent.

Referensi: `../wazzapagents/wazzapagent/README.md:7-12`, `../wazzapagents/wazzapagent/src/index.ts:45-84`, `../wazzapagents/wazzapagent/python/bridge/main.py:71-136`.

## Strategi

Rewrite dilakukan bertahap, bukan big-bang:

1. Bekukan perilaku lama sebagai baseline dan perbaiki contract drift.
2. Bangun fondasi Go, protocol compatibility harness, dan persistence read-only.
3. Pindahkan control plane dan bridge Python ke Go sambil mempertahankan Node/Baileys.
4. Jadikan Go pemilik tunggal migrasi dan durable delivery.
5. Evaluasi native Go WhatsApp melalui spike dan canary.
6. Hapus sidecar Node hanya setelah parity WhatsApp terbukti.

Keputusan default: **Node/Baileys tetap menjadi sidecar sementara**. Library WhatsApp Go tidak diasumsikan kompatibel dengan auth state, protobuf, LID/JID, interactive messages, atau reconnect Baileys.

## Sasaran akhir

- Satu service Go utama untuk config, account lifecycle, agent, LLM, jobs, persistence, control panel, dan observability.
- Adapter WhatsApp dapat memakai sidecar Node atau `hypermeow` tanpa mengubah domain/agent.
- Satu migration owner dengan versioned migrations.
- Typed event/action envelope dan schema machine-readable.
- Transactional inbox/outbox dan action receipts dengan retention.
- Stable `TenantID`; `folderPath` dipertahankan hanya untuk kompatibilitas.
- Reproducible build, health/readiness, backup, migration dry-run, dan rollback.

## Scope

### Termasuk

- Multi-account lifecycle dan tenant isolation.
- Protocol v2.0 dan seluruh frame aktual.
- Message normalization, context IDs, sender refs, group metadata, media, command, permission, moderation, activation, stickers, interactive messages.
- Batching, history, LLM1/LLM2, action extraction, ACK hydration, scheduler, direct invoke, sub-agent.
- Seluruh database, repositories, config, control panel, audit, deployment, dan observability.
- Compatibility, migration, canary, cutover, dan rollback tests.

### Tidak dilakukan pada tahap awal

- Mengubah schema/data existing secara destruktif.
- Menggabungkan semua database ketika behavior masih dipindahkan.
- Migrasi auth Baileys langsung ke native Go tanpa spike terpisah.
- Menghapus Node/Python sebelum replacement lulus parity gates.
- Menambah fitur produk baru yang tidak diperlukan untuk keamanan atau migrasi.

## Dokumen

1. [Baseline kompatibilitas](01-COMPATIBILITY-BASELINE.md)
2. [Arsitektur target](02-TARGET-ARCHITECTURE.md)
3. [Roadmap dan work breakdown](03-ROADMAP.md)
4. [Migrasi data](04-DATA-MIGRATION.md)
5. [Pengujian, cutover, dan rollback](05-TESTING-CUTOVER.md)
6. [Risiko dan keputusan](06-RISKS-DECISIONS.md)

## Definition of Done

Rewrite selesai ketika:

- seluruh behavior inventory memiliki test atau keputusan eksplisit untuk dihapus;
- compatibility suite lulus untuk protocol, HTTP API, DB, prompt/action, dan tenant isolation;
- Go menjadi satu-satunya owner state mutable selain auth store adapter WhatsApp;
- minimal dua tenant melewati canary dan burn-in tanpa cross-tenant leak atau duplicate side effect;
- backup/restore dan rollback telah diuji;
- Node dan Python dapat dilepas tanpa kehilangan fitur yang disepakati;
- lint, test, race test, vulnerability scan, dan build target production lulus.
