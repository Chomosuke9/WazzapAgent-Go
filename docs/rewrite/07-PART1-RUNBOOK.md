# Part 1 Canary Runbook

## Batas release

Part 1 adalah `v0.1-canary`, bukan stable release dan bukan pengganti otomatis WazzapAgent lama. Gunakan WhatsApp test account, tenant/account UUID, data directory, HTTP port, process/service, dan log destination yang khusus. Jangan menunjuk binary ini ke database atau session lama.

Canary nyata belum terbukti hanya karena unit test dan build lokal lulus. Catat pairing, reconnect, DM, group mention, prompt command, restart, kill switch, backup, dan rollback sebagai evidence terpisah.

## 1. Siapkan konfigurasi

Salin `.env.example` menjadi `.env`, lalu batasi permission file. Binary membaca process environment dan tidak memuat `.env` sendiri.

Generate dua UUID berbeda. Contoh Windows PowerShell:

```powershell
[guid]::NewGuid().ToString().ToLowerInvariant()
[guid]::NewGuid().ToString().ToLowerInvariant()
```

Contoh Linux:

```bash
uuidgen | tr '[:upper:]' '[:lower:]'
uuidgen | tr '[:upper:]' '[:lower:]'
chmod 600 .env
```

Untuk direct chat, provider address umumnya berupa nomor E.164 tanpa `+` diikuti `@s.whatsapp.net`. Group address berakhiran `@g.us`. Isi hanya owner dan chat test yang memang boleh memicu canary. Jangan kirim API key, QR pairing, owner JID, atau allowlist ke chat/log.

Minimum stage pairing:

```dotenv
WAZZAP_WHATSAPP_ENABLED=true
WAZZAP_AGENT_ENABLED=false
WAZZAP_PAIRING_OUTPUT=terminal
```

Walaupun response agent mati, Part 1 saat ini tetap memvalidasi owner, allowlist, dan konfigurasi LLM saat runtime WhatsApp dinyalakan. Isi semuanya sebelum startup.

Muat `.env` tanpa mencetak nilainya. Linux:

```bash
set -a
. ./.env
set +a
```

Windows PowerShell:

```powershell
foreach ($line in Get-Content -LiteralPath .env) {
    if ($line -match '^\s*([^#][^=]*)=(.*)$') {
        $name = $matches[1].Trim()
        $value = $matches[2].Trim().Trim('"')
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}
```

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

## 3. Pair dengan response agent mati

Jalankan dari terminal interaktif agar QR terlihat:

```text
go run ./cmd/wazzapagent
```

QR adalah secret sementara. Scan melalui WhatsApp > Linked devices dan jangan merekam atau membagikannya. Startup fresh device harus gagal tertutup bila `WAZZAP_PAIRING_OUTPUT=disabled`.

Probe dari host yang sama:

```text
GET http://127.0.0.1:8080/health/live
GET http://127.0.0.1:8080/health/ready
GET http://127.0.0.1:8080/metrics
```

`live` harus `200`. Saat pairing/connect belum selesai, `ready` harus `503`; setelah account `open`, harus `200` dengan `account_state: open`. Metrics tidak memiliki authentication, jadi pertahankan HTTP bind di loopback kecuali ada reverse proxy dan network policy yang sesuai.

Hentikan dengan Ctrl+C dan tunggu log `application stopped`. Ubah:

```dotenv
WAZZAP_PAIRING_OUTPUT=disabled
```

Restart. Session harus terbuka kembali tanpa QR dan readiness harus kembali `200`.

## 4. Backup awal

Lakukan backup ketika process sudah berhenti secara graceful. Salin seluruh directory berikut sebagai satu unit:

```text
<WAZZAP_DATA_DIR>/tenants/<WAZZAP_TENANT_ID>/
  app.db
  whatsapp.db
```

Simpan backup di lokasi private dan catat checksum. Jangan hanya menyalin salah satu database; application state dan device session memang memiliki ownership terpisah tetapi dibutuhkan bersama untuk restore canary.

## 5. Aktifkan bounded canary

Setelah pairing, reopen, health, dan backup lulus:

```dotenv
WAZZAP_AGENT_ENABLED=true
WAZZAP_PAIRING_OUTPUT=disabled
```

Restart diperlukan; Part 1 belum memiliki hot reload. Verifikasi berurutan:

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

Kill switch satu langkah adalah set `WAZZAP_AGENT_ENABLED=false`, lalu restart process. Runtime WhatsApp tetap dapat tersambung, tetapi policy menolak invocation dan send baru. Untuk memutus network/session runtime juga, set `WAZZAP_WHATSAPP_ENABLED=false` dan restart.

Jika `wazzap_delivery_unknown_total` naik, jangan mengirim ulang manual secara otomatis. Outcome berarti native send mungkin sudah diterima provider tetapi receipt lokal belum pasti. Hentikan canary, periksa WhatsApp tujuan dan state action, lalu reconcile secara manual. Part 1 sengaja tidak memiliki blind resend untuk state ini.

## 7. Rollback

1. Matikan response agent (`WAZZAP_AGENT_ENABLED=false`) dan restart, atau stop process bila dampaknya berlanjut.
2. Pertahankan data directory dan log sebagai evidence; jangan menghapus database untuk mencoba ulang.
3. Kembalikan hanya binary/config canary ke artifact sebelumnya bila perlu.
4. Probe bahwa service lama tetap berjalan dan state-nya tidak berubah.
5. Restore kedua database hanya saat process berhenti dan hanya dari backup yang satu set.

Stop canary bila pairing/reconnect gagal, duplicate reply terjadi, allowlist/owner salah, secret/content muncul di log, queue terus naik, state `unknown_outcome` tidak dapat direconcile, atau rollback probe gagal.

## 8. Retention

Worker maintenance berjalan segera saat startup dan kemudian setiap jam:

- content terminal pada inbound/action dihapus dari row setelah 24 jam;
- terminal turn beserta action/receipt terkait dihapus setelah 30 hari;
- turn yang masih pending/retryable tidak dihapus agar recovery tidak kehilangan payload.

Retention bukan backup. Tentukan kebijakan backup terenkripsi dan expiry backup secara terpisah sebelum canary menyimpan data nyata.
