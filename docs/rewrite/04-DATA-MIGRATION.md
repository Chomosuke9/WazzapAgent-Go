# Migrasi Data

## Sasaran

- Tidak kehilangan tenant settings, stats, moderation, jobs, memories, stickers, sub-agent completion, media, atau WhatsApp session.
- Tidak menjalankan tiga schema writers bersamaan.
- Setiap transform idempotent, resumable, dapat diverifikasi, dan memiliki rollback.
- Migrasi behavior dipisahkan dari konsolidasi database.

## State existing

Per tenant, state tersebar di:

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

Global state termasuk `.env`, `accounts.json`, dan audit JSONL. Existing DDL dibuat saat runtime oleh Node dan Python. Referensi: `../wazzapagents/wazzapagent/src/db/schema/index.ts:80-425`, `../wazzapagents/wazzapagent/python/bridge/db/core.py:548-806`.

SQLite file format kompatibel lintas driver, tetapi schema ownership, pragmas, concurrent writers, timestamp parsing, dan transaction semantics tetap harus diuji.

## Tahap 1 — Discovery dan backup

Untuk setiap tenant:

1. Resolve canonical root dan stable tenant ID.
2. Inventaris file, size, ownership, permissions, dan free disk.
3. Catat SQLite version, journal mode, schema SQL, indexes, columns, row counts.
4. Jalankan `quick_check`; `integrity_check` untuk maintenance window atau suspect DB.
5. Checkpoint WAL sebelum cold backup.
6. Salin seluruh tenant root dan global config/catalog/audit.
7. Buat manifest berisi timestamp, app compatibility version, checksums, dan row counts.
8. Uji restore ke isolated path.

Backup tidak valid sampai restore drill berhasil.

## Tahap 2 — Read-only compatibility

Go membuka copied production fixtures read-only dan membandingkan:

- exact repository outputs;
- NULL/default conversion;
- date/time units dan timezone;
- ordering/pagination;
- global fallback scope;
- participant/sender refs;
- activation expiry;
- model defaults;
- memories and mention bindings;
- tenant isolation.

Tidak ada `CREATE TABLE`, `ALTER TABLE`, PRAGMA mutating, atau recovery rewrite pada mode ini.

## Tahap 3 — Versioned migration framework

Tambahkan `schema_migrations` per database:

```sql
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at_ms INTEGER NOT NULL
);
```

Rules:

- SQL migrations embedded dan immutable setelah release.
- Checksum mismatch menghentikan startup.
- Satu process memegang migration lock per tenant.
- Backup dan disk-space check sebelum destructive table rebuild.
- `--dry-run` menampilkan current/target version dan expected operations.
- Repositories tidak boleh membuat/mengubah schema.
- Forward migrations lebih disukai additive; destructive cleanup ditunda.

Snapshot schema fresh dan setiap supported historical state harus berada di test fixtures.

## Tahap 4 — Ownership cutover

Urutan aman per tenant:

1. Set tenant ke maintenance/draining.
2. Stop Python session dan pastikan background task selesai/cancel sesuai semantics.
3. Stop Node DB writes atau jalankan Node hanya sebagai stateless WhatsApp adapter.
4. Checkpoint WAL dan ambil final backup.
5. Go mengambil migration lock.
6. Jalankan versioned migrations.
7. Reconcile row counts dan domain invariants.
8. Start Go writers.
9. Node sidecar hanya menerima canonical actions/events; tidak lagi migrate schema.
10. Monitor DB busy, WAL growth, write failures, dan domain counters.

Jangan membuka Go writer sebelum old writer ownership dihentikan secara eksplisit.

## Migrasi action receipts

Existing JSON receipts menyimpan request fingerprint dan state. Target table:

```sql
CREATE TABLE action_receipts (
  tenant_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  action TEXT NOT NULL,
  state TEXT NOT NULL,
  response_json TEXT,
  created_at_ms INTEGER NOT NULL,
  completed_at_ms INTEGER,
  expires_at_ms INTEGER NOT NULL,
  PRIMARY KEY (tenant_id, request_id)
);
CREATE INDEX action_receipts_expiry_idx
  ON action_receipts(expires_at_ms);
```

Migration:

- parse JSON dalam compatibility importer;
- validate tenant/request/fingerprint/state;
- import dengan `INSERT ... ON CONFLICT` idempotent;
- quarantine malformed entries, jangan silently drop;
- preserve completed response replay;
- define handling untuk legacy `running`: default fail-closed/manual reconciliation agar side effect tidak otomatis diulang;
- retain original file read-only sampai rollback window selesai;
- prune expired receipts dalam bounded batches.

Untuk action baru, transaction claim harus terjadi sebelum side effect. Exact-once tidak dapat dijamin lintas WhatsApp network; target adalah at-most-once local execution dengan reconciliation untuk unknown outcomes.

## Migrasi sub-agent tracker

Target relational state minimal:

- tasks/sessions;
- progress entries;
- completion payload ownership;
- output metadata/path/hash/status;
- delivery intents/checkpoints;
- delivered tombstones;
- retry/dead-letter metadata.

Importer wajib:

- preserve finalized-but-undelivered completions;
- materialize inline outputs atomically;
- verify path stays under tenant spool;
- verify size/SHA when tersedia;
- preserve deferred callbacks dan delivered tombstones;
- avoid re-delivering completed checkpoints;
- emit reconciliation report.

Original JSON tidak dihapus sampai all pending items delivered/discarded dan rollback window berakhir.

## Scheduler data

- Preserve task IDs, chat IDs, prompts, creation times, `fire_at_ms`, dan daily local time.
- Store timezone/offset policy explicitly untuk task baru.
- Past-due one-shot di-claim sekali setelah readiness.
- Shutdown cancellation tidak menandai task complete.
- Daily next-fire dihitung setelah claim outcome tanpa menghapus row.
- Add claim owner, claim timestamp, lease expiry, attempts, dan last error bila multi-instance mungkin digunakan.

## Auth state

Baileys multi-file auth dan hypermeow sqlstore tidak diasumsikan kompatibel.

Pilihan:

1. **Sidecar retained:** tidak ada auth migration; Node tetap memiliki `<tenant>/auth`.
2. **Native re-pair:** backup auth lama, buat native store terpisah, operator re-pair, dan pertahankan rollback ke sidecar.
3. **Auth importer:** hanya jika spike membuktikan mapping credentials/key material lengkap dan diuji reconnect/device sync.

Milestone 7 selalu memakai pilihan 1: Node sidecar tetap menjadi pemilik auth walaupun Go sudah menjadi agent dan migration owner. Milestone 8 wajib menghasilkan ADR auth, dan Milestone 9 tidak boleh dimulai tanpa memilih serta menguji pilihan 1, 2, atau 3 untuk setiap tenant canary.

Jangan menimpa `auth/` lama. Gunakan path/store baru sampai native canary selesai.

## Stable tenant identity

Tambahkan mapping:

```text
tenant_id -> canonical_root -> legacy_folder_path -> account credential/slot
```

- `tenant_id` immutable.
- Path dapat dipindah tanpa mengganti identity.
- Protocol v2 masih mengirim `folderPath` selama compatibility period.
- Validate root containment dan symlink/reparse-point behavior.
- Account ID API tidak boleh menjadi arbitrary filesystem path decoder.

## Secrets

Secrets saat ini dapat berada di `.env` dan `llm_provider_config` plaintext.

Migration policy:

- jangan log values;
- API hanya mengembalikan masked/set-state;
- file permissions dibatasi;
- sediakan secret provider abstraction untuk environment/file/platform secret;
- encrypt-at-rest hanya jika key management tersedia; jangan memakai key yang disimpan di file yang sama;
- rotate WS/control/sub-agent/direct-invoke tokens saat cutover bila exposure diragukan.

## Schema consolidation

Jangan gabungkan split DB pada cutover behavior. Setelah stabil, evaluasi ADR terpisah:

- tetap split untuk compatibility/failure isolation; atau
- satu tenant DB untuk atomic cross-domain transactions dan simpler backup.

Jika digabung, lakukan via copy-and-verify ke file baru, bukan in-place destructive migration.

## Invariants

Setelah setiap migration:

- semua DB lulus `quick_check`;
- schema version/checksum sesuai;
- row count expected atau delta dijelaskan;
- tepat satu default model per intended scope;
- activation code uniqueness dan chat activation linkage valid;
- memory IDs unique;
- no cross-tenant paths/rows;
- scheduled IDs unique dan due-state valid;
- no delivered sub-agent output kembali pending;
- no receipt request ID dengan conflicting fingerprint;
- file output berada di tenant root yang benar.

## Rollback

Rollback tidak berarti reverse SQL otomatis.

1. Stop Go intake dan drain/capture state.
2. Simpan forensic copy state Go.
3. Restore pre-cutover tenant backup ke isolated/original path sesuai runbook.
4. Start old Node/Python dengan pinned version dan old auth.
5. Verify handshake, WhatsApp open, DB checks, jobs, dan representative actions.
6. Reconcile side effects yang outcome-nya unknown selama window cutover.

Rollback gate: schema additive boleh dibaca old runtime atau restore wajib dilakukan. Migration yang membuang/rename field tidak boleh masuk sebelum rollback window ditutup.
