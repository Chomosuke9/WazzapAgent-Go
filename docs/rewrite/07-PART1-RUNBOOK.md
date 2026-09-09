# Part 1 Canary Runbook

## Batas release

Part 1 adalah `v0.1-canary`, bukan stable release dan bukan pengganti otomatis WazzapAgent lama. Gunakan WhatsApp test account, data directory, HTTP port, process/service, dan log destination yang khusus. Jangan menunjuk binary ini ke database atau session lama.

Canary nyata belum terbukti hanya karena unit test dan build lokal lulus. Catat pairing, reconnect, DM, group mention, prompt command, restart, kill switch, backup, dan rollback sebagai evidence terpisah.

## 1. Siapkan konfigurasi

Salin `.env.example` menjadi `.env`, lalu batasi permission file. Binary membaca `.env` saat startup dari working directory process. Jika sebuah key sudah ada di process environment, nilai file tidak dipakai—termasuk ketika nilai process kosong; default atau validasi typed config kemudian tetap berlaku.

Pengguna tidak perlu membuat UUID. Pada startup pertama, binary membuat `TenantID` dan `AccountID`, lalu menyimpannya dalam `<WAZZAP_DATA_DIR>/runtime-identity.json`. File ini menjadi source of truth dan wajib ikut backup. Runtime lama yang masih memiliki `WAZZAP_TENANT_ID` dan `WAZZAP_ACCOUNT_ID` akan mengadopsi nilai tersebut ketika file identity belum ada; jalankan binary baru sekali sebelum menghapus kedua variable lama. Nilai yang berbeda dari identity durable akan ditolak agar runtime tidak membuka state tenant/account yang salah.

Pada Linux/Android, batasi permission `.env`:

```bash
chmod 600 .env
```

Untuk direct chat, provider address umumnya berupa nomor E.164 tanpa `+` diikuti `@s.whatsapp.net`. Group address berakhiran `@g.us`. Isi hanya owner dan chat test yang memang boleh memicu canary. Jangan kirim API key, QR pairing, owner JID, atau allowlist ke chat/log.

Jalur normal tidak memerlukan enable/pairing variable: WhatsApp dan response Agent aktif secara default, sedangkan QR ditampilkan di terminal hanya untuk session baru. Karena Agent aktif segera setelah pairing, pastikan allowlist sudah sempit dan benar sebelum scan QR.

Jalankan binary dari directory yang berisi `.env`; file tidak perlu di-`source`. Parser mendukung baris kosong, komentar satu baris, prefix `export`, nilai tanpa quote, serta nilai dengan single/double quote. Parser tidak menjalankan shell expansion.

Jika service manager atau Android menjalankan binary dengan working directory berbeda, set `WAZZAP_ENV_FILE` **di environment process**, bukan di dalam `.env`, ke path absolut. File yang ditunjuk secara eksplisit wajib ada.

Linux/Android Termux:

```bash
export WAZZAP_ENV_FILE=/path/absolut/ke/.env
./wazzapagent
```

Windows PowerShell:

```powershell
$env:WAZZAP_ENV_FILE = (Resolve-Path -LiteralPath .env).Path
.\dist\wazzapagent.exe
```

Tanpa `WAZZAP_ENV_FILE`, tidak adanya `.env` bukan error sehingga deployment environment-only tetap dapat berjalan. Perubahan `.env` baru berlaku setelah process direstart; konfigurasi tidak di-inject ke binary saat build.

## 2. Quality gate dan build

```text
gofmt -l .
go mod verify
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
govulncheck ./...
```

`gofmt -l .` wajib tidak menghasilkan output. Simpan source commit, `go version`, target, dan checksum artifact yang benar-benar dideploy.

Windows PowerShell build example:

```powershell
New-Item -ItemType Directory -Force -Path dist | Out-Null
go build -trimpath -o dist/wazzapagent.exe ./cmd/wazzapagent
Get-FileHash -Algorithm SHA256 dist/wazzapagent.exe
```

## 3. Pair dan verifikasi reconnect

Jalankan dari terminal interaktif agar QR terlihat:

```text
go run ./cmd/wazzapagent
```

QR adalah secret sementara. Scan melalui WhatsApp > Linked devices dan jangan merekam atau membagikannya. Untuk deployment non-interaktif yang sudah memiliki session, operator dapat memakai override opsional `WAZZAP_PAIRING_OUTPUT=disabled`; fresh device kemudian akan gagal tertutup tanpa mencetak QR.

Probe dari host yang sama:

```text
GET http://127.0.0.1:8080/health/live
GET http://127.0.0.1:8080/health/ready
GET http://127.0.0.1:8080/metrics
```

`live` harus `200`. Saat pairing/connect belum selesai, `ready` harus `503`; setelah account `open`, harus `200` dengan `account_state: open`. Metrics tidak memiliki authentication, jadi pertahankan HTTP bind di loopback kecuali ada reverse proxy dan network policy yang sesuai.

Hentikan dengan Ctrl+C dan tunggu log `application stopped`, lalu restart tanpa mengubah `.env`. Session harus terbuka kembali tanpa QR dan readiness harus kembali `200`.

## 4. Backup awal

Lakukan backup ketika process sudah berhenti secara graceful. Salin seluruh data directory berikut sebagai satu unit:

```text
<WAZZAP_DATA_DIR>/
  runtime-identity.json
  tenants/
    <generated-tenant-id>/
      app.db
      whatsapp.db
```

Simpan backup di lokasi private dan catat checksum. Jangan hanya menyalin salah satu database atau mengabaikan `runtime-identity.json`; application state, device session, dan durable identity dibutuhkan bersama untuk restore canary.

## 5. Jalankan bounded canary

Setelah pairing, reopen, health, dan backup lulus, jalankan bounded canary tanpa perubahan enable flag. Verifikasi berurutan:

1. Pesan dari direct chat di luar allowlist tidak mendapat balasan.
2. Text dari direct chat allowlisted mendapat tepat satu text reply.
3. Group allowlisted tanpa mention bot tidak mendapat balasan.
4. Group allowlisted dengan mention bot mendapat tepat satu text reply.
5. Pesan `fromMe`, status/broadcast, edit, dan media-only tidak memicu LLM/send.
6. Owner menjalankan `/prompt view`, `/prompt set Balas sangat singkat.`, lalu `/prompt clear`.
7. Non-owner yang mencoba `/prompt set` tidak mengubah config.
8. Restart setelah satu balasan tidak mengirim ulang action yang telah selesai.

Jangan menguji dengan chat atau account pengguna produksi. Pantau minimal:

- `wazzap_inbound_queue_depth` tidak menetap dekat capacity;
- `wazzap_model_failures_total` dan `wazzap_model_timeouts_total`;
- `wazzap_delivery_failed_total` dan `wazzap_delivery_unknown_total`;
- jumlah goroutine tidak tumbuh tanpa batas;
- log hanya memuat operation/error code, bukan message body, API key, JID, atau QR.

## 6. Kill switch dan unknown outcome

Untuk operasi normal, tiga variable runtime tidak perlu ditulis. Kill switch opsional tetap tersedia: set `WAZZAP_AGENT_ENABLED=false`, lalu restart process. Runtime WhatsApp tetap dapat tersambung, tetapi policy menolak invocation dan send baru. Set `WAZZAP_WHATSAPP_ENABLED=false` untuk mematikan runtime WhatsApp sekaligus Agent. Perubahan config tetap memerlukan restart karena Part 1 belum memiliki hot reload.

Jika `wazzap_delivery_unknown_total` naik, jangan mengirim ulang manual secara otomatis. Outcome berarti native send mungkin sudah diterima provider tetapi receipt lokal belum pasti. Hentikan canary, periksa WhatsApp tujuan dan state action, lalu reconcile secara manual. Part 1 sengaja tidak memiliki blind resend untuk state ini.

## 7. Rollback

1. Matikan response agent (`WAZZAP_AGENT_ENABLED=false`) dan restart, atau stop process bila dampaknya berlanjut.
2. Pertahankan data directory dan log sebagai evidence; jangan menghapus database untuk mencoba ulang.
3. Kembalikan hanya binary/config canary ke artifact sebelumnya bila perlu.
4. Probe bahwa service lama tetap berjalan dan state-nya tidak berubah.
5. Restore seluruh data directory, termasuk identity dan kedua database, hanya saat process berhenti dan hanya dari backup yang satu set.

Stop canary bila pairing/reconnect gagal, duplicate reply terjadi, allowlist/owner salah, secret/content muncul di log, queue terus naik, state `unknown_outcome` tidak dapat direconcile, atau rollback probe gagal.

## 8. Retention

Worker maintenance berjalan segera saat startup dan kemudian setiap jam:

- content terminal pada inbound/action dihapus dari row setelah 24 jam;
- terminal turn beserta action/receipt terkait dihapus setelah 30 hari;
- turn yang masih pending/retryable tidak dihapus agar recovery tidak kehilangan payload.

Retention bukan backup. Tentukan kebijakan backup terenkripsi dan expiry backup secara terpisah sebelum canary menyimpan data nyata.
