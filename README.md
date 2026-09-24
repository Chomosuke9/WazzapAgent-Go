# WazzapAgent Go

Greenfield rewrite WazzapAgent sebagai satu modular monolith Go. Part 3 menyediakan conversation core durable: transcript canonical penuh untuk chat yang allowlisted, kebijakan eksternal, satu `Agent` per chat, bounded model context, LLM OpenAI-compatible, lalu pengiriman balasan dan typed effect melalui outbox terpisah.

Status saat ini: shell Wails P0, lifecycle shared core P1, settings persistence GUI P2, pengelolaan sesi P3, serta Start/Stop/Apply Agent dari UI P4 sudah terhubung secara lokal. Proyek native Android telah menghasilkan APK debug arm64 pada host Linux, tetapi perilaku pada perangkat belum diuji. Exit gate real-device/production canary belum selesai, sehingga statusnya belum stable release. Canary wajib memakai account, data directory, port, dan allowlist khusus; project lama tidak disentuh.

UI React yang sama juga tersedia di browser melalui `cmd/server`. Jalankan `wails3 task web:build`, lalu `wails3 task web:run`, dan buka `http://127.0.0.1:8080`. Server hanya menerima koneksi loopback; lihat [panduan mode web](cmd/server/README.md) untuk akses dari perangkat lain. Android memakai shell WebView native dan data privat perangkat; lihat [panduan build Android](build/android/README.md). Build dan uji native Linux tetap memiliki gate tersendiri.

## Bentuk OOP

`Agent` adalah aggregate chat-scoped untuk satu `(TenantID, AccountID, ChatID)`:

```go
current, err := registry.AgentFor(ctx, key)
snapshot, err := current.Config().Refresh(ctx)
result, err := current.Invoke(ctx, invocation)
```

- `Agent.Invoke` menjaga serialisasi per chat, idempotency turn, snapshot config, model invocation, dan dispatch dari action yang sudah disimpan.
- `Agent.Config()` adalah child object versioned dengan mutation CAS dan explicit change notification.
- `Agent.History()` adalah child object durable dengan immutable paging, idempotent append, config-version guarded read/reset, reset tombstone, dan retention.
- `AgentRegistry` menjamin satu live Agent per key, coalescing concurrent construction, hard limit, dan idle eviction.
- Actor verification, trigger, allowlist, dan permission check sengaja berada di `internal/inbound` dan `internal/policy`, di luar object Agent.
- SQLite adalah source of truth; object/cache dapat direkonstruksi setelah restart.

Kontrak lengkap ada di [docs/rewrite/04-AGENT-CONTRACT.md](docs/rewrite/04-AGENT-CONTRACT.md). Kontrak Part 3 untuk principal, capability, command registry, dan typed effect ada di [docs/rewrite/09-PART3-CONTRACT.md](docs/rewrite/09-PART3-CONTRACT.md).

## Part 1 sampai Part 3

Termasuk:

- fresh QR pairing dan persistent Hypermeow device session;
- eligible DM dan group mention/reply text; semua inbound text/sticker pada chat allowlisted tetap masuk transcript, sedangkan group pasif tidak memicu respons;
- LID sebagai canonical sender identity; nomor telepon hanya alias addressing/policy, dan mapping durable `senderRef ⇄ LID` diverifikasi dua arah sebelum transaksi intake commit;
- dua handler serta worker pool independen untuk command dan AI; model yang macet tidak menghabiskan worker command;
- durable dedup, opaque per-chat `senderRef` (6 karakter lowercase base36), generation lease, action outbox, receipt, dan restart recovery;
- `/prompt view`, `/prompt set <teks>`, dan `/prompt clear`, hanya untuk configured owner;
- full durable transcript untuk chat allowlisted (DM dan group), termasuk pesan group pasif yang tidak memicu balasan; model tetap menerima bounded context yang bertahan setelah restart;
- setiap entry memiliki sequence ordering, timestamp, opaque senderRef, dan canonical quote metadata; sticker text-only disimpan sebagai placeholder `【sticker】`;
- deterministic typed-provenance context builder; sender name, quote, dan message text tetap diperlakukan sebagai untrusted data;
- canonical quoted-message lookup serta group trigger melalui mention atau reply ke bot;
- durable per-chat debounce/batching dengan burst cap dan stale-context guard;
- recovery turn lama tetap diproses lebih dahulu; pesan baru tidak menyalip turn yang masih generating, retryable, atau menunggu delivery;
- `/help`, `/info`, owner-only `/dump` untuk menampilkan input context Agent yang benar-benar dibangun, serta owner-only `/reset`;
- owner-only `/permission [view|0|1|2|3]`: level 0 tanpa moderasi, level 1 delete, level 2 delete+mute, dan level 3 delete+mute+kick;
- `/trigger` dapat digunakan owner atau admin, hanya di grup dan selalu menolak pesan bot: atur pemicu per chat dengan `/trigger mention on|off`, `/trigger name on|off`, `/trigger reply on|off`, dan `/trigger regex on|off`. Nama Agent dicocokkan tanpa membedakan kapital; `/trigger pattern <regex>` otomatis mengaktifkan pemicu nama dengan regex Go khusus. Pengaturan yang sama tersedia di panel Pengaturan chat;
- model selalu memperoleh `reply_message` dan `react_to_message`; delete/mute/kick tetap command keluarga `/group *` yang dibawa secara silent oleh `reply_message`, bukan tool terpisah;
- mark-read dan composing presence otomatis pada AI lane, tanpa permission atau model tool;
- typed effect recovery untuk reaction dan model-generated `/group *`; operasi durable ambigu menjadi `unknown_outcome`, sedangkan read/presence yang ephemeral tidak direplay;
- optional one-hop fallback LLM untuk timeout/rate-limit/provider failure; fallback tidak pernah mengulang native effect;
- checksum-verified offline backup/restore serta history retention;
- fail-closed allowlist, external send reauthorization, response kill switch, bounded queues/concurrency/timeouts;
- `/health/live`, `/health/ready`, dan Prometheus text `/metrics` pada loopback secara default;
- scrub content terminal setelah 24 jam, bounded history, dan hapus terminal turn/action setelah 30 hari.

Command ditulis satu file per command di [`internal/inbound/commands`](internal/inbound/commands). Setiap file mengekspor satu `command.Descriptor` berisi metadata, `Permission`, capability, dan handler. Semua descriptor di-inject, termasuk command berbahaya; `Permission` dievaluasi untuk setiap invocation, misalnya `"(isPrivate or isAdmin or isOwner) and !fromMe"` untuk memblokir bot. `go generate ./...` memindai folder tersebut dan membuat `registry_gen.go`; CI selalu menjalankan generator lalu menolak perubahan generated file yang belum disimpan. Jadi setelah menambah command, jalankan generator sebelum build atau commit:

```text
go generate ./...
go test ./...
go build ./cmd/wazzapagent
```

Go tidak menjalankan `go generate` otomatis saat `go build` biasa. Jika command baru belum digenerate, binary masih memakai registry generated terakhir.

Belum termasuk media, keluarga command lain di luar moderasi `/group delete|mute|kick`, scheduler, sub-agent, control panel, multi-account product surface, atau stable production release.

Transcript mulai dibangun sejak event diterima oleh rewrite ini; tidak ada backfill otomatis dari riwayat provider. Balasan model dan command dicatat sebagai assistant entry, tetapi pesan outgoing manual yang dikirim di luar action outbox belum diimpor. Model context memakai renderer transcript compact (`【id】 HH:MM`, `REPLYING TO`, dan `sender 【senderRef】: text`) dalam satu final history block agar hemat token; setiap pesan tidak menjadi provider message terpisah. `senderRef` berbentuk tepat 6 karakter lowercase base36; format lain tidak diterima. Metadata durable tetap disimpan terstruktur di SQLite.

## Mulai

Persyaratan: Go toolchain sesuai `go.mod`, satu dedicated test account WhatsApp, dan endpoint chat-completions yang OpenAI-compatible.

```text
copy .env.example .env
go generate ./...
go test ./...
go build ./cmd/wazzapagent
```

Backup/restore harus dijalankan saat runtime berhenti:

```text
wazzapagent backup <destination-parent>
wazzapagent verify-backup <backup-directory>
wazzapagent restore-backup <backup-directory> <new-data-directory>
```

Saat startup, binary otomatis membaca `.env` dari working directory. Environment process tetap menjadi prioritas; set `WAZZAP_ENV_FILE` pada process bila file berada di lokasi lain. Tenant/account UUID dibuat otomatis sekali di data directory, WhatsApp dan Agent aktif secara default, dan QR otomatis ditampilkan di terminal hanya ketika session belum ter-pair. Ikuti [runbook Part 1](docs/rewrite/07-PART1-RUNBOOK.md) untuk pairing awal, [runbook Part 2](docs/rewrite/08-PART2-RUNBOOK.md) untuk history/batching/backup/restore, serta [contract Part 3](docs/rewrite/09-PART3-CONTRACT.md) sebelum mengaktifkan capability model.

## Dokumentasi

- [Panduan aplikasi multiplatform](docs/multiplatform/README.md) (P0-P4 tersedia secara lokal; native Android, migrasi data lama, dan canary perangkat nyata masih tersisa)
- [Runbook testing manual](docs/multiplatform/TESTING.md)
- [Master plan](PLAN.md)
- [Target architecture](docs/rewrite/02-TARGET-ARCHITECTURE.md)
- [Roadmap](docs/rewrite/03-ROADMAP.md)
- [Agent contract](docs/rewrite/04-AGENT-CONTRACT.md)
- [Testing and release](docs/rewrite/05-TESTING-RELEASE.md)
- [Part 1 operator runbook](docs/rewrite/07-PART1-RUNBOOK.md)
- [Part 2 operator runbook](docs/rewrite/08-PART2-RUNBOOK.md)
- [Part 3 authority and typed effect contract](docs/rewrite/09-PART3-CONTRACT.md)
