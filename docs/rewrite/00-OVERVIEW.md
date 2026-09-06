# Rencana WazzapAgent Go

## Tujuan

Membangun WazzapAgent baru di repository ini dengan Go. Project `../wazzapagents/wazzapagent` hanya menjadi referensi fitur dan perilaku produk; data, database, konfigurasi, auth state, serta deployment lama tidak dimigrasikan.

Referensi produk lama:

- gateway Node.js/Baileys untuk WhatsApp, command, media, dan control panel;
- bridge Python untuk batching, LLM1/LLM2, history, scheduler, direct invoke, dan sub-agent.

Referensi: `../wazzapagents/wazzapagent/README.md:7-12`.

## Strategi

1. Inventaris fitur yang ingin dipertahankan.
2. Tetapkan kontrak, schema, dan arsitektur baru yang idiomatik untuk Go.
3. Bangun native WhatsApp adapter dengan `hypermeow` sejak awal.
4. Bangun persistence baru dengan versioned schema.
5. Implement agent, jobs, sub-agent, dan control panel secara bertahap.
6. Uji dengan account dan data baru, lalu rilis sebagai aplikasi baru.

Tidak ada compatibility requirement terhadap protocol Node/Python, bentuk database lama, raw Baileys response, path tenant lama, atau auth state lama. Bentuk lama boleh dipakai sebagai fixture pembanding perilaku, bukan kontrak runtime.

## Sasaran akhir

- Satu service Go untuk WhatsApp, config, account lifecycle, agent, LLM, jobs, persistence, control panel, dan observability.
- Native `hypermeow` adapter di belakang domain interface.
- Stable `TenantID` dan layout data baru.
- Versioned schema sejak versi pertama.
- Transactional inbox/outbox, action receipts, scheduler claims, dan sub-agent state.
- Reproducible build, health/readiness, backup/restore, dan release runbook.

## Scope

### Termasuk

- Multi-account lifecycle dan tenant isolation.
- Message normalization, context IDs, sender refs, group metadata, media, commands, permissions, moderation, activation, stickers, dan interactive messages.
- Batching, history, LLM1/LLM2, action extraction, ACK hydration internal, scheduler, direct invoke, dan sub-agent.
- Database baru, repositories, config, control panel, audit, deployment, dan observability.
- Unit, integration, security, real-device, load, and release tests.

### Tidak termasuk

- Import SQLite, JSON state, media, sticker, config, atau audit lama.
- Import auth Baileys; semua account melakukan pairing baru.
- Menjalankan Node atau Python sebagai sidecar.
- Menjaga protocol v2 atau control panel API lama secara byte-for-byte.
- Rollback ke runtime lama sebagai bagian release project baru.

## Dokumen

1. [Baseline fitur](01-FEATURE-BASELINE.md)
2. [Arsitektur target](02-TARGET-ARCHITECTURE.md)
3. [Roadmap dan work breakdown](03-ROADMAP.md)
4. Dihapus — project greenfield, tanpa migrasi data lama
5. [Pengujian dan release](05-TESTING-RELEASE.md)
6. [Risiko dan keputusan](06-RISKS-DECISIONS.md)

## Definition of Done

Project siap rilis ketika:

- seluruh fitur scope memiliki acceptance test atau keputusan eksplisit untuk ditunda;
- native WhatsApp capability matrix lulus untuk account baru;
- tenant isolation dan durable side-effect tests lulus;
- fresh install, pairing, backup/restore, upgrade schema internal, dan restart recovery teruji;
- lint, test, race test, vulnerability scan, dan production build lulus;
- tidak ada dependency runtime pada source Node/Python lama.
