# Part 2 Reliable Conversation Runbook

## Batas release

Part 2 menambahkan conversation core text-only yang durable. Implementasinya tersedia untuk verifikasi lokal, tetapi belum menjadi stable release dan belum dianggap production-complete sebelum real-device canary, forced process-kill, network-loss observation, serta restore drill di bawah lulus.

Gunakan dedicated WhatsApp test account, data directory, HTTP port, process/service, log destination, dan allowlist. Jangan arahkan binary ini ke session atau database project WazzapAgent lama. Upgrade yang dimaksud dalam dokumen ini hanya dari data WazzapAgent-Go Part 1.

## 1. Backup sebelum upgrade

Stop runtime Part 1 secara graceful terlebih dahulu. Binary Part 2 dapat membuat backup tanpa membuka atau memigrasikan database:

```text
wazzapagent backup <destination-parent>
wazzapagent verify-backup <backup-directory-yang-dicetak>
```

Perintah `backup` memuat `.env` dengan aturan runtime yang sama, tetapi hanya memvalidasi `WAZZAP_DATA_DIR`; credential LLM dan setting live lain tidak diperlukan. Jalankan dari working directory yang berisi `.env`, atau set `WAZZAP_ENV_FILE` pada process. Destination parent harus berada di luar data directory.

Backup menyalin seluruh data root sebagai satu set, termasuk `runtime-identity.json`, application SQLite, dan Hypermeow device SQLite. Manifest menyimpan ukuran serta SHA-256 setiap file. Jangan menjalankan backup ketika runtime masih hidup; kedua database terpisah harus diambil pada operational point yang sama.

Setelah backup terverifikasi, jalankan binary Part 2. Migration `002_part2_history.sql` dan `003_full_transcript_quotes.sql` diterapkan otomatis, lalu migration checksum lama diverifikasi. Jangan mengubah migration yang sudah pernah diterapkan.

## 2. Konfigurasi Part 2

Nilai default baru di `.env.example`:

```dotenv
WAZZAP_MESSAGE_DEBOUNCE=350ms
WAZZAP_MESSAGE_BURST_CAP=8
WAZZAP_HISTORY_WINDOW=64
WAZZAP_MAX_CONTEXT_BYTES=65536
WAZZAP_HISTORY_KEEP_LATEST=256
WAZZAP_HISTORY_MAX_AGE=720h
```

- `MESSAGE_DEBOUNCE` adalah quiet window per chat sebelum batch diklaim. Nilainya harus positif dan maksimal satu menit.
- `MESSAGE_BURST_CAP` membatasi jumlah pesan dalam satu invocation. Remainder tetap durable dan diproses sebagai batch berikutnya.
- `HISTORY_WINDOW` membatasi jumlah entry yang dibaca untuk satu model invocation; ini tidak memotong transcript durable yang tersimpan.
- `MAX_CONTEXT_BYTES` membatasi hasil context builder setelah serialisasi; logical invocation lama (user beserta assistant reply-nya) dibuang bersama, bukan dipotong atau dibuat orphan.
- `HISTORY_KEEP_LATEST` dan `HISTORY_MAX_AGE` bekerja bersama: entry lama baru dihapus ketika berada di luar jumlah terbaru dan melewati usia maksimum. Assistant history `pending` dipertahankan; `unknown` dapat menua seperti terminal entry lain karena anti-resend tombstone berada pada turn/action receipt.

Transcript canonical berlaku untuk chat yang allowlisted sejak event diterima runtime ini. Storage menyimpan seluruh inbound text, text-only sticker placeholder, serta assistant reply dari model/command sampai retention menghapusnya. History provider sebelum pairing/rewrite tidak di-backfill, dan outgoing manual yang tidak melewati action outbox belum terdeteksi.

Semua nilai dibaca saat runtime startup. Perubahan memerlukan restart.

## 3. Perilaku yang harus terlihat

- Beberapa text yang tiba berdekatan pada chat yang sama digabung menjadi satu invocation setelah quiet window, tetapi tetap menjadi entry user terpisah dengan sender provenance masing-masing.
- Hanya assistant response yang delivery-nya terkonfirmasi `succeeded` yang dipakai kembali sebagai model history. Pending, failed, dan unknown output tidak dianggap sudah dilihat pengguna.
- Quote diterjemahkan dari provider message ID menjadi internal `MessageID` dalam scope tenant/account/chat. Raw JID dan provider ID tidak dikirim ke model.
- Di group allowlisted, bot dipicu oleh mention atau reply terhadap response bot yang dapat di-resolve. Group message biasa tetap tidak memicu response, tetapi tetap disimpan sebagai transcript dan akan terlihat pada invocation eligible berikutnya.
- Setiap entry memiliki sequence monotonik, timestamp, senderRef opaque, dan quote ke internal MessageID/sequence. Sticker yang belum didukung sebagai media masuk sebagai placeholder `【sticker】` agar chronology tidak hilang.
- Context selalu diakhiri current user message. Jika durable message yang lebih baru sudah ada, invocation lama gagal tertutup sebagai stale context.
- Turn conversation lama yang masih received, generating, retryable, planned, atau delivery-pending menghalangi batch baru. Urutan memakai durable intake sequence, bukan UUID acak ketika timestamp sama. Recovery mempertahankan batch anchor; row Part 1 tanpa anchor diperlakukan sebagai singleton agar ordering tidak dilompati.
- Durable turn/action replay memakai ID yang sama dan tidak memanggil model atau mengirim effect pengganti secara buta.

Control commands:

```text
/help
/info
/reset
/prompt view
/prompt set <teks>
/prompt clear
```

`/reset` dan mutation `/prompt` hanya boleh dijalankan configured owner. Authorization dilakukan di inbound/policy layer; method `Agent.History().Reset()` dan Config setters tidak menerima actor. `/reset` menulis tombstone, mengosongkan context yang terlihat, dan mencegah pre-reset staged/generating message muncul kembali akibat race atau restart.

## 4. Local quality gate

Jalankan dari repository root:

```text
gofmt -l .
go mod verify
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
govulncheck ./...
```

Selain build host, build `CGO_ENABLED=0` untuk Windows amd64, Linux amd64, Linux arm64, dan Android arm64. Simpan commit, versi Go, target, serta checksum artifact yang benar-benar dipakai canary.

## 5. Real-device canary matrix

Jalankan probe secara berurutan dan catat timestamp serta hasil tanpa menyimpan JID, nomor telepon, API key, QR, atau isi chat ke repository.

1. Restart dari session Part 1 dan pastikan pairing tetap terbuka tanpa QR baru.
2. Kirim dua DM cepat dari allowlisted chat. Harus ada satu response setelah debounce, bukan dua response.
3. Kirim follow-up yang bergantung pada dua pesan tadi. Jawaban harus menunjukkan context tersambung.
4. Restart process, lalu kirim follow-up lain. History harus tetap tersambung dan response lama tidak terkirim ulang.
5. Di group allowlisted, reply langsung ke balasan bot tanpa mention. Harus ada tepat satu response.
6. Kirim group text tanpa mention dan tanpa reply. Tidak boleh ada response saat itu. Kirim mention berikutnya; context dan `History.List` harus memuat text pasif sebelumnya.
7. Kirim sticker di group, lalu mention. Model harus melihat placeholder `【sticker】` dengan sequence/timestamp, tanpa raw protobuf.
8. Jalankan `/info`; pastikan model, config version, dan status history tampil tanpa prompt, JID, atau secret.
9. Jalankan `/reset` sebagai non-owner; history tidak boleh berubah. Jalankan sebagai owner; follow-up berikutnya harus mulai dengan context kosong.
10. Kirim burst lebih besar dari cap. Semua pesan harus masuk ke batch berurutan tanpa hilang dan tanpa satu batch melewati cap.
11. Putuskan network sebelum native send dimulai; recovery boleh mencoba action yang belum mulai.
12. Putuskan network atau kill process setelah native send mulai tetapi sebelum receipt tersimpan. Setelah lease habis, outcome harus `unknown`; jangan mengirim ulang otomatis.
13. Replay provider event yang sama. Jumlah visible response dan action ID tidak boleh bertambah.

Stop canary segera bila ordering berubah, pesan hilang, duplicate response terlihat, allowlist dilanggar, pre-reset context kembali, atau content/secret muncul di log.

## 6. Backup/restore drill

Stop runtime secara graceful, lalu:

```text
wazzapagent backup <destination-parent>
wazzapagent verify-backup <backup-directory>
wazzapagent restore-backup <backup-directory> <new-data-directory>
```

Restore hanya menerima destination directory baru yang belum ada; command tidak pernah menimpa data directory aktif. Arahkan salinan `.env` canary ke directory hasil restore, gunakan port/process terpisah, lalu verifikasi identity, session reopen, `/info`, history follow-up, dan tidak adanya replay send. Jangan menukar directory production sampai drill terisolasi lulus.

Jika verification mendeteksi file tambahan, file hilang, symlink, ukuran berubah, atau checksum mismatch, backup ditolak.

## 7. Observability dan recovery

Pantau endpoint loopback `/metrics`, khususnya:

- `wazzap_inbound_batches_total` dan `wazzap_inbound_batched_messages_total`;
- `wazzap_history_resets_total`;
- `wazzap_model_failures_total` dan `wazzap_model_timeouts_total`;
- `wazzap_delivery_pending_total`, `wazzap_delivery_failed_total`, dan `wazzap_delivery_unknown_total`;
- `wazzap_inbound_queue_depth` serta `wazzap_process_goroutines`.

Inbound recovery melanjutkan claim yang stale/due. Action recovery hanya mengeksekusi intent durable dengan `ActionID` yang sama. Action yang crash sebelum native send dapat dicoba setelah lease; action yang lease-nya habis dalam state executing dipindahkan ke `unknown_outcome` dan tidak di-resend secara buta.

## 8. Rollback

1. Stop process Part 2; pertahankan data directory dan log sebagai evidence.
2. Jangan jalankan binary Part 1 terhadap database yang sudah dimigrasikan Part 2.
3. Untuk rollback data, restore backup pre-upgrade ke directory baru.
4. Jalankan artifact Part 1 dengan `.env` salinan yang menunjuk directory hasil restore dan port/process terpisah.
5. Verifikasi identity, session, health, dan satu bounded allowlisted probe sebelum mengganti service canary.

Part 2 tidak menyediakan media, model tools, scheduler, sub-agent, generic model-generated commands, atau stable multi-account product surface. Kemampuan tersebut tetap milik Part berikutnya.
