# Panduan implementasi aplikasi multiplatform

Status: shell P0 sudah diuji di Windows, lifecycle shared core P1, settings/
app-data P2, sesi WhatsApp GUI P3, dan Apply/runtime UI P4 tersedia secara lokal.
GUI memakai composition pipeline headless yang sama; native host Android dan
verifikasi perangkat nyata masih tersisa, jadi status multiplatform belum selesai.
Backend CLI tetap tersedia.

Dokumen ini adalah spesifikasi kerja untuk model pelaksana. Nama file bertanda
**Baru** adalah target implementasi, bukan klaim bahwa file tersebut sudah ada.
Kerjakan per paket kerja di bawah; jangan langsung mengimplementasikan seluruh
roadmap. Laporan implementasi aktual tersedia pada bagian 7-11; peta file di bawah
tetap menjelaskan target akhir, termasuk pekerjaan yang belum diimplementasikan.

Urutan baca: struktur -> keputusan tetap -> kontrak data/API -> peta perubahan
per file -> paket kerja dan acceptance gate -> format laporan pelaksana.

- [Aturan kerja](#1-aturan-kerja-model-pelaksana)
- [Temuan kode existing](#2-temuan-kode-yang-wajib-ditangani)
- [Keputusan dan kontrak](#3-keputusan-arsitektur-yang-sudah-ditetapkan)
- [Peta perubahan per file](#4-peta-file-isi-baru-dan-perubahan-file-existing)
- [Paket kerja dan verifikasi](#5-paket-kerja-dan-acceptance-gate)
- [Template handoff](#6-format-handoff-untuk-model-pelaksana)
- [Hasil implementasi P0](#7-hasil-implementasi-p0-21-september-2026)
- [Hasil implementasi P1](#8-hasil-implementasi-p1-21-september-2026)
- [Fondasi implementasi P2](#9-fondasi-implementasi-p2-21-september-2026)
- [Hasil implementasi P3](#10-hasil-implementasi-p3-pengelolaan-sesi-whatsapp-23-september-2026)
- [Hasil implementasi P4](#11-hasil-implementasi-p4-runtime-agent-dan-apply-melalui-ui-23-september-2026)
- [Runbook testing manual](TESTING.md)

## Struktur

```text
WazzapAgent-Go/
├── cmd/
│   ├── wazzapagent/             # CLI/headless yang sudah ada
│   ├── app/                     # Bootstrap Wails desktop/mobile (baru)
│   └── server/                  # Reservasi mode web berikutnya (baru)
├── internal/
│   ├── app/                     # Composition runtime yang sudah ada
│   ├── config/                  # Konfigurasi dan validasi yang sudah ada
│   ├── control/                 # Use case pengelolaan aplikasi (baru)
│   ├── platform/                # Path data dan lifecycle OS (baru)
│   ├── adapters/
│   │   ├── wails/               # Bindings, DTO, dan event UI (baru)
│   │   ├── sqlite/              # Persistence aplikasi yang sudah ada
│   │   ├── whatsapp/hypermeow/   # Integrasi WhatsApp yang sudah ada
│   │   └── llm/                 # Integrasi LLM yang sudah ada
│   └── ...                      # Agent, account, inbound, policy, dll.
├── frontend/                    # Satu frontend bersama (baru)
│   ├── bindings/                # Keluaran generator Wails
│   ├── public/
│   └── src/
│       ├── assets/
│       ├── components/
│       ├── pages/
│       ├── layouts/
│       ├── hooks/
│       ├── services/
│       └── styles/
├── build/                       # Input build/packaging (baru)
│   ├── windows/
│   ├── linux/
│   ├── darwin/
│   └── android/
├── docs/
│   ├── rewrite/                 # Kontrak backend yang sudah ada
│   └── multiplatform/           # Dokumentasi integrasi aplikasi (baru)
├── go.mod                       # Satu module Go yang sudah ada
└── go.sum
```

Folder kosong memiliki `.gitkeep` supaya strukturnya ikut version control.
Hapus placeholder ketika folder sudah memiliki file implementasi.

## Batas dependency

```text
React -> generated Wails binding -> adapter Wails -> control use case
                                                       |
                                             core Go / repository
                                                       |
                                               SQLite / Hypermeow
```

- `cmd/app` merakit shell dan dependency; `cmd/wazzapagent` tetap bootstrap CLI.
- `internal/app` menangani composition runtime, bukan detail UI.
- `internal/control` menangani settings/session/runtime tanpa dependency Wails.
- `internal/adapters/wails` adalah satu batas komunikasi Go dengan UI.
- `internal/platform` memusatkan kebutuhan OS; detail native mengikuti build target.
- `internal/adapters/sqlite` tetap tempat SQL; tidak membuat layer database paralel.
- Interface repository ditambahkan dekat use case yang membutuhkannya, bukan
  membuat generic repository atau menyalin semua package ke struktur baru.
- Frontend tidak membaca database, memegang client WhatsApp, atau menjalankan
  logic permission. Wails IPC tidak membutuhkan HTTP server localhost.
- `cmd/server` baru diisi pada tahap web dengan use case Go yang sama.

Nama `cmd/app` memperjelas bahwa entrypoint Wails ditujukan untuk desktop dan
Android. Ketika scaffold resmi diintegrasikan, konfigurasi build dan generator
harus diarahkan ke package tersebut. Native bootstrap Android mengikuti template
resmi yang dipin dan memakai core Go yang sama.

## Data persistent

Folder source/build tidak menjadi tempat data pengguna. Resolver di
`internal/platform` memilih direktori data sesuai OS; desktop menyimpan pointer
bootstrap di config directory tetap agar data root dapat dipindah tanpa mengubah
lokasi config aplikasi.

```text
<platform-app-data>/
├── settings.db                  # Settings GUI typed + session state
├── runtime-identity.json        # Format identitas runtime yang sudah ada
└── tenants/
    └── <tenant-id>/
        ├── app.db
        └── whatsapp.db
```

`settings.db` menyimpan konfigurasi aplikasi sebelum runtime WhatsApp/Agent siap.
Sesi WhatsApp tetap dimiliki Hypermeow; riwayat dan outbox tetap memakai store
aplikasi yang sudah ada. Stop tidak sama dengan Logout atau Hapus Data.

Settings DB sekarang dibuat saat bootstrap GUI dan tidak memulai runtime WhatsApp
secara otomatis. Tahap ini tidak memindahkan database conversation, mengimpor
`.env` ke secret store, atau mengubah schema tenant existing. Adopsi data instalasi adalah
langkah terpisah yang harus menjaga identitas dan konsistensi seluruh database.

## 1. Aturan kerja model pelaksana

1. Baca `git status --short`, dokumen ini, dan kode yang disebut dalam paket kerja
   sebelum mengedit. Pertahankan perubahan lain yang sudah ada di working tree.
2. Implementasikan hanya paket kerja yang ditugaskan dan prerequisite yang sudah
   disepakati. Jangan membuat stub sukses, UI dengan status koneksi palsu, atau
   test yang melewati fitur belum selesai untuk membuat build tampak hijau.
3. Nama simbol baru di dokumen ini adalah kontrak aplikasi yang direncanakan,
   bukan API Wails yang boleh diasumsikan tersedia. Untuk API Wails/native,
   periksa source/template versi yang dipin sebelum menulis pemanggilannya.
4. Jangan menambahkan alias, shim, pembaca format legacy, atau migrasi dari stack
   Node/Python. Penambahan settings baru dan pemakaian ulang data Go saat ini
   berada dalam lingkup pekerjaan ini; jangan mereset data pengguna otomatis.
5. Jangan menjalankan runtime, pairing, provider LLM, atau restore terhadap data
   asli untuk unit test. Gunakan temporary directory dan dependency fake.
6. Jangan membaca/menyalin nilai secret ke dokumentasi, fixture, screenshot, log,
   frontend bundle, atau laporan. Jangan commit `.env`/database/signing key.
7. Jika API/toolchain tidak mendukung rencana, catat bukti dan batasannya. Jangan
   mengganti Wails, SQLite driver, protokol WhatsApp, atau desain persistence
   secara diam-diam. Lanjutkan bagian independen yang masih bisa diverifikasi.
8. Setelah setiap paket kerja, isi laporan pada bagian terakhir. Status build,
   unit test, native smoke test, dan pengujian WhatsApp nyata harus terpisah.

## 2. Temuan kode yang wajib ditangani

Baseline diperiksa pada 2026-09-20. Tabel ini mencatat temuan awal; sebagian
sudah ditangani pada laporan P0/P1 di akhir dokumen. Verifikasi current code
sebelum mengerjakan setiap paket, jangan mengulang perubahan yang sudah selesai.

| File/simbol sekarang | Masalah untuk aplikasi GUI | Perubahan yang diwajibkan |
| --- | --- | --- |
| `internal/app/app.go`: `Application.Run` | Selalu memanggil `net.Listen`; startup dan shutdown terikat HTTP | Pisahkan listener diagnostik dari runtime; GUI bisa hidup tanpa port HTTP |
| `internal/app/app.go`: `composeRuntime` | Merakit LLM, registry, dispatcher, dan WhatsApp bersama | Sediakan mode session-only tanpa LLM, handler pesan, atau worker Agent |
| `internal/app/app.go`: `SetSystemPolicy` | Policy global mutable, diisi entrypoint CLI | Ganti dependency constructor dan satu sumber embedded prompt bersama |
| `internal/config/config.go`: `validateEnabled` | WhatsApp enabled mewajibkan owner, allowlist, LLM, dan prompt | Pisahkan validasi draft, sesi, serta runtime Agent |
| `internal/config/dotenv.go`: `embeddedEnv` | Konfigurasi lokal ikut ter-embed ke binary | Hapus embedding konfigurasi secret; GUI memakai settings DB, CLI memakai file/environment eksplisit |
| `internal/config/runtime_identity.go`: `runtimeIdentityMu` | Mutex hanya melindungi satu proses | Tambahkan ownership lock data root lintas proses sebelum membuat identity/membuka DB |
| `internal/adapters/whatsapp/hypermeow/adapter.go`: `Open`, `Start` | Wajib target store, owner, allowlist, dan handler meski hanya pairing | Bedakan session-only dari bot mode; jangan membuat dummy owner/allowlist atau handler palsu |
| `internal/adapters/whatsapp/hypermeow/adapter.go`: `Stop` | Menutup container dan membuat instance tidak bisa dipakai ulang | Restart membuat instance baru setelah instance lama benar-benar selesai |
| `internal/adapters/sqlite/config.go`: `LoadOrCreate` | Default baru tidak memperbarui chat yang sudah ada | Rekonsiliasi field global sebelum worker runtime baru dijalankan |
| `internal/adapters/sqlite/inbound.go`: `ReconcileAccountPolicy` | Menyinkronkan owner/allowlist durable | Tetap jalankan sebelum intake/delivery pada bot mode setelah perubahan policy |
| `internal/account/runtime.go`: `StatePairing`, `Run` | State pairing belum menjadi progres UI lengkap; event connector dikonsumsi runtime | Tambahkan progres terstruktur; UI tidak boleh membaca channel yang sama dan mencuri event |
| `.github/workflows/ci.yml` | `go build ./cmd/...`, termasuk cross-build `CGO_ENABLED=0` | Pisahkan build CLI/core dari native GUI; binary Go Android bukan APK |

## 3. Keputusan arsitektur yang sudah ditetapkan

| ID | Keputusan |
| --- | --- |
| D01 | Satu Go module, satu React frontend. `cmd/app` menjadi shell Wails bersama; `cmd/wazzapagent` tetap CLI; `cmd/server` belum diimplementasikan. |
| D02 | Domain tidak mengimpor Wails. `control` mendefinisikan port use case; `app` mengimplementasikan composition/runtime port itu. Entrypoint merakit keduanya, tanpa import cycle. |
| D03 | GUI membaca `settings.db` + default Go, tanpa implicit override dari environment atau `.env` working directory. CLI tetap memakai precedence process env > file eksplisit/default `.env` > default Go. |
| D04 | Nilai environment yang relevan dapat diimpor/diekspor secara eksplisit dari UI. Settings GUI tidak ditulis ulang ke process environment menggunakan `os.Setenv`. |
| D05 | Satu controller dan satu runtime owner per data root. Wails single-instance saja tidak cukup karena CLI juga dapat membuka data yang sama. |
| D06 | Save menyimpan; Apply menerapkan dengan stop/rebuild/start. Tidak membuat hot reload field runtime pada increment pertama. |
| D07 | Pairing/session-only tidak membuat LLM client, mengeksekusi command, menyimpan inbound baru, atau mengirim respons. Bot mode memakai pipeline lama. Pesan selama session-only tidak dijanjikan akan di-backfill. |
| D08 | Sesi, settings, identity, history, dan outbox disimpan backend. React context hanya menyimpan draft/progres UI. QR/kode pairing tidak durable. |
| D09 | Secret tersimpan backend dengan proteksi direktori OS. SQLite biasa bukan encrypted vault; OS keychain/encrypted vault tidak menjadi prerequisite tahap pertama. |
| D10 | Untuk source GUI saja gunakan build tag proyek `gui`; core/headless tetap dapat diuji tanpa WebView toolchain atau frontend bundle. Tag ini tambahan proyek, bukan tag bawaan Wails. |
| D11 | Default GUI membuka setup tanpa otomatis mengaktifkan bot. Tambahkan preferensi `startOnLaunch` default false; jika true, auto-start hanya ketika konfigurasi lengkap dan sesi masih valid. Tidak membuka QR otomatis saat boot. |
| D12 | Stop mempertahankan sesi. Quit menjalankan shutdown. Logout menghapus autentikasi lewat library. Hapus data bukan bagian implisit dari ketiganya. |
| D13 | Desktop Quit menghentikan bot. Tray/autostart OS ditunda. Android background memerlukan paket kerja khusus, bukan sekadar goroutine saat Activity tertutup. |

Nomor paket kerja menentukan urutan implementasi, bukan kewajiban membuat semua
file sekaligus. P1 membuat port runtime/controller minimal yang dibutuhkan
composition; P2 menambah settings; P3 melengkapi operasi sesi. Sampai suatu mode
tersedia, jangan mengekspos tombol aktif atau melaporkan operasi itu sukses.
P0 cukup mendaftarkan GetAppInfo dan event demonstrasi tanpa membuka settings
repository yang belum dibuat. P2 menambahkan form settings minimal untuk uji
persistence; P3 menambahkan panel pairing minimal untuk verifikasi perangkat.
P5 melengkapi seluruh halaman dan responsivitas. Dengan demikian, tabel file
menjelaskan bentuk akhir tanpa memaksa satu paket bergantung pada kode masa depan.

### 3.1 Kontrak sumber konfigurasi

- `config.Settings` baru: typed values tanpa ketergantungan Wails/env/SQL; boleh
  belum lengkap untuk first launch. `config.Snapshot` tetap konfigurasi runtime
  immutable yang hanya dibentuk setelah validasi sesuai mode.
- `DefaultSettings`, `ValidateDraft`, `ValidateSession`, `ValidateAgent`, dan
  pembentukan snapshot menggunakan aturan/default yang sama. Jangan salin aturan
  kedua di React atau mengubahnya dengan convert-to-map environment sementara.
- Draft boleh menyimpan field wajib yang belum diisi, tetapi menolak nilai yang
  terisi namun salah tipe/range/format. Endpoint+key fallback yang belum lengkap
  boleh menjadi draft dengan readiness issue; bot tidak boleh start sebelum valid.
- Session validation memeriksa data root, identity, dan parameter koneksi. Owner,
  allowlist, base prompt, serta kredensial LLM baru wajib pada Agent validation.
- `AgentEnabled=true` dengan `WhatsAppEnabled=false` adalah kombinasi tidak valid.
  UI mematikan Agent ketika pengguna mematikan WhatsApp, dan backend memvalidasi
  pasangan nilai tersebut; tidak hanya mengandalkan state checkbox.
- Default angka mengikuti konstanta Go yang sekarang. Nilai contoh `.env` yang
  berbeda adalah contoh tuning, bukan default UI. Impor mempertahankan nilainya.
- `ASSISTANT_NAME` yang kosong boleh disimpan sebagai draft, tetapi bot mode harus
  menolaknya jika context builder tidak dapat menggunakan nama kosong.
- `WAZZAP_BASE_PROMPT` tetap editable; system policy ter-embed yang tidak dapat
  di-override tetap menjadi file terpisah dan tidak ditawarkan sebagai setting.

### 3.2 Inventaris field: tidak boleh ada setting yang hilang

`R` = diterapkan dengan restart runtime, `B` = bootstrap/data operation,
`C` = CLI/server-only, `I` = identitas read-only. Semua nilai tersimpan yang belum
aktif harus ditandai demikian di UI. Field secret menggunakan aksi khusus.

| Kategori | Key yang harus dipetakan | Perilaku |
| --- | --- | --- |
| Assistant | `ASSISTANT_NAME`, `WAZZAP_BASE_PROMPT` | R |
| Aktivasi | `WAZZAP_WHATSAPP_ENABLED`, `WAZZAP_AGENT_ENABLED` | R |
| Akses chat | `WAZZAP_OWNER_JID`, `WAZZAP_CHAT_ALLOWLIST` | R; gunakan parser/semantik allowlist existing |
| Provider utama | `WAZZAP_LLM_ENDPOINT`, `WAZZAP_LLM_API_KEY`, `WAZZAP_LLM_MODEL`, `WAZZAP_LLM_PROVIDER_ID` | R; API key secret |
| Fallback | `WAZZAP_LLM_FALLBACK_ENDPOINT`, `WAZZAP_LLM_FALLBACK_API_KEY` | R; model mengikuti provider utama seperti kontrak sekarang |
| LLM limits | `WAZZAP_LLM_TIMEOUT`, `WAZZAP_LLM_CONCURRENCY`, `WAZZAP_MAX_OUTPUT_TOKENS`, `WAZZAP_MAX_RESPONSE_BYTES` | R |
| Konteks | `WAZZAP_HISTORY_WINDOW`, `WAZZAP_MAX_CONTEXT_BYTES` | R |
| Retention | `WAZZAP_HISTORY_KEEP_LATEST`, `WAZZAP_HISTORY_MAX_AGE` | R; jelaskan efek pengurangan retention sebelum Apply |
| Inbound | `WAZZAP_INBOUND_QUEUE`, `WAZZAP_INBOUND_WORKERS` | R |
| Command | `WAZZAP_COMMAND_QUEUE`, `WAZZAP_COMMAND_WORKERS` | R |
| AI workers | `WAZZAP_AI_QUEUE`, `WAZZAP_AI_WORKERS` | R |
| Batching | `WAZZAP_MESSAGE_DEBOUNCE`, `WAZZAP_MESSAGE_BURST_CAP` | R |
| Registry | `WAZZAP_AGENT_MAX_LIVE`, `WAZZAP_AGENT_IDLE_TTL`, `WAZZAP_AGENT_CONSTRUCTION_TIMEOUT` | R |
| Koneksi | `WAZZAP_CONNECT_TIMEOUT`, `WAZZAP_SEND_TIMEOUT`, `WAZZAP_SHUTDOWN_TIMEOUT` | R |
| Policy | `WAZZAP_POLICY_ID`, `WAZZAP_POLICY_REVISION` | R; bukan moderation level per-chat |
| Observability | `WAZZAP_LOG_LEVEL`, `WAZZAP_LOG_FORMAT`, `LANGSMITH_API_KEY` | R; LangSmith key kosong berarti tracing nonaktif |
| Storage | `WAZZAP_DATA_DIR` | B; tampilkan lokasi efektif, ubah lewat operasi data-root khusus |
| Sumber file | `WAZZAP_ENV_FILE` | C; pemilih file impor pada GUI, bukan field yang mengubah sumber GUI |
| HTTP | `WAZZAP_HTTP_ADDRESS` | C; boleh diedit/disimpan untuk ekspor, tidak membuka listener GUI |
| Pairing output | `WAZZAP_PAIRING_OUTPUT` | C; GUI selalu UI, terminal/disabled tetap pilihan CLI |
| Identitas | `WAZZAP_TENANT_ID`, `WAZZAP_ACCOUNT_ID` | I; loader runtime sekarang memakai identity file, jangan impor ID ini sebagai edit settings |
| Warna log | `NO_COLOR`, `FORCE_COLOR` | C; tetap opsi terminal, tidak mengubah tema UI |
| Preferensi aplikasi | `startOnLaunch` | Settings GUI baru, tanpa menciptakan env alias yang tidak diperlukan |

Katalog field berisi key, tipe, group, default, batas nilai, penanda sensitif,
scope, dan apply mode. Pertahankan typed struct serta typed request; katalog
bukan alasan menambah reflection framework atau generic `map[string]any`.
Test coverage katalog membandingkan daftar key yang didukung loader dengan
field dan pengecualian eksplisit di atas, bukan sekadar menghitung input form.

### 3.3 Settings DB dan secret

- `settings.db` terpisah dari tenant `app.db`, sehingga setup selalu dapat dibuka.
- Buat `SettingsStore` dengan `Load` dan `Save(expectedRevision, values)`; Save
  memakai transaksi CAS dan menaikkan revision. Stale editor mendapat conflict,
  tidak menimpa perubahan secara last-write-wins.
- Schema awal sederhana: singleton `application_settings` dengan `id=1`,
  `revision`, `values_json`, `updated_at_ms`. JSON adalah serialisasi struct typed,
  bukan dumping seluruh environment. Gunakan constraint positif untuk revision,
  batas ukuran input, dan validasi saat decode. Jangan simpan riwayat API key.
- Tabel `session_state` juga singleton: active tenant/account ID, WhatsApp account
  identity (nullable sebelum paired), state (`unpaired`, `paired`, `revoked`),
  pending tenant/account ID (keduanya null atau keduanya terisi), serta updatedAt.
  Pending scope berarti rotasi sudah disiapkan tetapi belum difinalisasi; schema
  constraint mencegah pasangan ID parsial. Tidak perlu tabel riwayat sesi.
  Rotation tidak menaikkan revision konfigurasi yang sedang diedit pengguna.
- Migration settings memiliki ledger/checksum sendiri pada settings DB. Jangan
  menjalankan seluruh migration conversation pada settings DB atau mengedit SQL
  migration existing yang sudah pernah diaplikasikan.
- Tampilan settings hanya mengirim `SecretStatus{configured}`. Request secret
  menggunakan aksi `keep`, `replace`, atau `clear`; replace wajib nilai nonkosong.
  Mask `********` bukan nilai yang boleh tersimpan sebagai secret.
- Endpoint dan JID boleh ditampilkan pada form lokal, tetapi tidak menjadi log
  rutin/telemetry. Error validasi menyebut field dan aturan, bukan nilainya.
- Durasi pada DTO memakai string Go duration (`60s`, `15m`); revision/policy uint64
  pada batas JS memakai string desimal agar tidak kehilangan presisi.
- Impor `.env` menggunakan parser bounded yang ada, melaporkan unknown key,
  duplicate key, dan key bootstrap/read-only. Tidak menjalankan file sebagai shell.
  Preview menyamarkan secret; token impor sementara tidak durable dan kedaluwarsa.
- Ekspor lewat file picker; default menghilangkan key rahasia, bukan menulis mask.
  Sertakan secret hanya melalui pilihan eksplisit. Uji round-trip prompt multiline,
  quote, backslash, Unicode, nilai kosong, dan fallback disabled.

### 3.4 Runtime, operasi, dan event UI

Port berikut dimiliki `internal/control/ports.go`. Nama method dapat dipadatkan
ketika implementasi, tetapi tanggung jawab dan arah dependency harus tetap.

| Port/type | Kontrak yang harus tersedia |
| --- | --- |
| `SettingsRepository` | Load snapshot settings+revision; Save dengan expected revision |
| `SessionBindingRepository` | Load binding aktif, mark bound/revoked, begin/commit scope rotation dalam transaksi; dipisahkan dari revision settings yang sedang diedit pengguna |
| `RuntimeFactory` | Membuat satu runtime dari snapshot immutable, mode session-only/bot, pairing request, serta event sink |
| `ManagedRuntime` | Run dengan cancellation, Close idempotent, snapshot, dan operasi session melalui interface typed |
| `EventSink` | Publikasi status/hasil operasi tanpa menunggu JavaScript; tidak boleh memblokir provider callback |
| `DataRootLease` | Kepemilikan eksklusif data root sampai seluruh pemakai database tutup |

`app` mengimplementasikan runtime port. Adapter Wails membungkus controller dan
menerjemahkan DTO; controller tidak memanggil Wails global maupun concrete SQLite.
UI hanya mendapat service berikut:

| Method use case | Input/hasil dan aturan |
| --- | --- |
| `GetAppInfo` | Nama, versi build, platform, dan capability yang benar-benar tersedia; dipakai binding pertama pada P0 |
| `GetStatus` | Snapshot process/runtime, mode, account state, sessionPresent, savedRevision, activeRevision, pendingChanges, operasi terakhir, error aman |
| `GetSettings`, `GetSettingsSchema` | Values publik, status secret, revision, metadata form/readiness |
| `ValidateSettings` | Draft+secret actions; daftar field errors dan kesiapan session/Agent; tidak menulis |
| `SaveSettings` | Expected revision+patch typed; hasil revision baru dan perubahan yang belum aktif |
| `ApplySettings` | Expected saved revision; operation ID; restart hanya jika runtime sedang aktif |
| `Start`, `Stop`, `Reconnect` | Operation ID; Start memakai saved settings terbaru; Stop saat stopped sukses idempotent |
| `BeginPairing` | Metode QR/phone-code; phone hanya bila diperlukan; operation ID; menolak jika sesi valid sudah ada |
| `CancelPairing` | Cancellation khusus pairing, bukan menghapus sesi yang sudah berhasil tersimpan |
| `GetPairingStatus` | Secret pairing yang masih berlaku + generation/expiry, hanya di memori; diperlukan setelah UI reload |
| `Logout` | Operation ID; unlink lewat library, status gagal/sukses berbeda; tidak delete history |
| `PreviewEnvImport`, `CommitEnvImport`, `ExportEnv` | Native file action pada adapter; controller menerima data bounded dan revision/token, tidak arbitrary filesystem API |
| `Backup`, `Restore`, `ChangeDataRoot` | Operasi offline khusus setelah paket data selesai; jangan expose tombol aktif lebih awal |

Binding Wails yang sebenarnya harus mengikuti dukungan context/error generator
versi terpilih. Jangan expose interface port atau object `*sql.DB` sebagai service.
Context request yang selesai mengembalikan operation ID tidak boleh membatalkan
runtime; long-running operation memakai lifecycle context milik controller.

Event minimum: `runtime:status`, `settings:changed`, `pairing:changed`,
`operation:finished`. Sertakan process instance ID, operation ID bila ada, dan
sequence untuk membuang event lama; runtime ID baru ketika rebuild. UI subscribe,
lalu membaca snapshot, melakukan unsubscribe saat unmount, dan membaca ulang
ketika resume. Snapshot memiliki sequence agar respons lambat tidak menimpa event
baru. Status operasi terakhir juga tersedia lewat `GetStatus` jika event terlewat.
Pairing state memakai generation ID dan expiresAt; hasil lama tidak boleh
menampilkan kembali QR yang sudah habis. Jangan broadcast pesan WhatsApp mentah.

### 3.5 Algoritme Save, Apply, dan recovery

1. Serialisasikan mutasi controller (Save/Apply/Start/Stop/pairing/logout).
   `GetStatus` tetap cepat; jangan memegang mutex status selama operasi jaringan.
2. Save melakukan validasi draft dan CAS DB. Tidak mengubah runtime aktif.
   Bila ada Apply berjalan, Save berikutnya boleh ditolak dengan `busy` yang jelas.
3. Apply memverifikasi expected revision, memuat satu snapshot lengkap beserta
   secret di backend, lalu memvalidasi mode yang akan dijalankan sebelum stop.
4. Jika runtime stopped: konfigurasi tetap tersimpan untuk Start berikutnya;
   jangan start bot diam-diam. `activeRevision` kosong ketika tidak ada runtime.
5. Jika runtime aktif: hentikan admission, batalkan/drain worker sesuai timeout,
   tunggu Run selesai, lalu Close resource. Jangan restart jika shutdown timeout
   meninggalkan worker lama masih berjalan; laporkan failed dan tahan ownership.
6. Pada bot mode, buka store; transaksi rekonsiliasi semua chat dalam account itu:
   provider ID, model, token limit, base prompt, policy ID/revision mengikuti
   settings. Pertahankan PromptOverride, moderation level, history, dan turn.
   Naikkan config version hanya pada row yang benar-benar berubah.
7. Rekonsiliasi owner/allowlist dengan mekanisme existing. Semua rekonsiliasi
   selesai sebelum recovery worker/intake baru boleh berjalan.
8. Buat runtime baru dan start. `activeRevision` baru ditandai aktif sesudah
   startup siap; status WhatsApp tetap terpisah (connecting/open/reconnecting).
9. Kegagalan meninggalkan settings terbaru tersimpan, runtime failed/stopped,
   dan UI tetap terbuka. Tidak otomatis menghapus database atau kembali ke secret lama.
10. Saat proses restart, selalu rekonsiliasi projection global sebelum bot start.
    Operasi harus idempotent dan tidak bump version jika values sama. Dengan ini,
    crash di antara transaksi settings DB dan app DB dapat dipulihkan tanpa
    transaksi lintas database atau journal workflow tambahan.

`savedRevision` berasal dari DB; `activeRevision` merupakan fakta runtime hidup,
bukan nilai yang boleh dipulihkan sebagai connected setelah reboot. Turn/outbox
lama tetap menggunakan kontrak recovery dan pemeriksaan ulang policy yang ada;
jangan mengedit intent durable agar cocok dengan model/prompt baru.

### 3.6 Kontrak sesi dan pergantian akun

- Satu Hypermeow client aktif per data root. Session-only dan bot mode menggunakan
  construction path yang sama dengan mode eksplisit, bukan dua library/client paralel.
- Session-only tidak memerlukan target resolver/handler pesan. Abaikan event
  message sebelum normalisasi/intake; jangan membuang pesan dengan handler fake.
  Bot mode tetap menolak owner/allowlist/handler yang tidak valid.
- GetQRChannel dipasang sebelum connect. Phone pairing memakai `PairPhone` milik
  dependency terpasang dan urutan yang didukung source library; tidak ada custom
  WhatsApp protocol. Buktikan dengan perangkat, termasuk flow pada HP yang sama.
- Setelah pairing sukses, akhiri operasi pairing pada session-only. Aktivasi bot
  berikutnya adalah Start/Apply eksplisit dengan Agent validation.
- Reconnect dan Start membuat adapter baru setelah Stop menutup adapter sebelumnya.
  Network loss sementara memakai reconnect library, bukan menghapus device store.
- Logout: blokir intake/outbound dan hentikan worker bot; pertahankan client cukup
  lama untuk melakukan unlink; baru tutup client/container. Jangan panggil Stop
  yang menutup client sebelum API Logout. Gagal unlink tidak dianggap sukses.
- Setelah logout/permanent revocation, old history tetap ada. Pairing akun lain
  memakai scope baru. Karena path DB sekarang tenant-scoped, rotasikan pasangan
  tenant+account untuk sesi pengganti; jangan hanya ganti account ID di folder lama.
- Buat penanda durable sesi yang pernah terikat di settings DB (scope, normalized
  account identity, dan revoked/logout state; tanpa credential). Bila old identity
  ada tetapi session sudah hilang, jangan pair ke scope lama secara otomatis.
  Penanda/identity update harus dapat dilanjutkan setelah crash; sebelum membuka
  worker, cocokkan scope dan device identity. Scope lama tidak dihapus.
- Untuk adopsi data Go existing, device store yang valid menjadi sumber binding
  account pertama; jangan menganggap install lama yang belum memiliki penanda
  sebagai alasan menghapus sesi atau merotasi identity aktif.

## 4. Peta file: isi baru dan perubahan file existing

### 4.1 Scaffold, entrypoint, asset, dan build

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `go.mod`, `go.sum` | Ubah | Tambahkan versi Wails v3 yang dipin. Pertahankan Go minimum dan versi Hypermeow/SQLite kecuali spike membuktikan konflik. Tidak membuat nested module. |
| `Taskfile.yml` | Baru | Task frontend install/typecheck/build, generate bindings, desktop dev/build, Android build/run. Arahkan package ke `./cmd/app`; semua GUI task menyertakan tag `gui` bersama tag resmi template. Catat command yang benar-benar diuji. |
| `build/config.yml`, `build/Taskfile.yml` | Baru dari template | Nama WazzapAgent, metadata aplikasi, entrypoint, output `bin`, asset path, serta versi tooling yang sama. Isi field sesuai schema versi template; jangan menebak schema YAML. |
| `cmd/app/main.go` | Baru, `gui` | Bootstrap lifecycle Wails; resolve app data, acquire lease, open settings, buat controller, register service, buat window, load asset. Window/setup harus terbuka walaupun settings tidak lengkap. |
| `cmd/app/bootstrap.go` | Baru, `gui` | Constructor composition untuk shell: logger, settings repository, runtime factory, event adapter, native file adapter. Error storage menghasilkan layar recovery tanpa membuat DB kosong pengganti. |
| `frontend/assets.go` | Baru, `gui` | Package Go `frontend`, embed `all:dist`, expose asset FS untuk shell. Build frontend terlebih dahulu. Jangan memakai `//go:embed ../../frontend/dist` karena parent path tidak valid. |
| `cmd/wazzapagent/main.go` | Ubah | Tetap environment/file loader dan signal-aware CLI; resolve root tanpa efek samping, acquire lease, baru load/create identity dan store. Gunakan core runtime baru, terminal pairing adapter, shared system policy, dan diagnostics server khusus CLI. Backup command juga memakai lease. |
| `cmd/wazzapagent/main_test.go` | Ubah | Pertahankan exit code invalid config/offline command; pindahkan prompt-render tests ke pemilik prompt bersama. |
| `cmd/server/README.md` | Pertahankan | Web belum aktif. Jangan isi server, auth framework, atau transport HTTP frontend pada tahap ini. |
| `.gitignore` | Ubah seperlunya | Ignore output/cache/local SDK config. Tetap track template build, package lock, dan generated bindings sesuai kebijakan CI; jangan ignore seluruh `build/`. |

Wails default root entrypoint tidak boleh diasumsikan otomatis menemukan
`cmd/app`. Spike harus membuktikan dev, binding generation, production asset,
dan Android entrypoint path. Jika template versi terpilih memerlukan penempatan
berbeda, catat perubahan path yang diperlukan sebelum memindahkan file.

### 4.2 Core runtime dan policy bersama

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `internal/app/app.go` | Ubah | Ekstrak listener HTTP, composition detail, dan shared prompt global. Sisakan runtime factory/lifecycle facade yang jelas; hilangkan ketergantungan langsung terminal/`os.Stdout` melalui injection. |
| `internal/app/runtime.go` | Baru | Pemilik Run/Close, mode session-only/bot, cancellation, status, resource cleanup dan shutdown ordering. Close idempotent; rebuild menghasilkan instance baru. |
| `internal/app/compose.go` | Baru | Pindahkan `composeRuntime` tanpa menulis ulang domain. Bot branch merakit pipeline existing; session-only branch tidak membuat LLM/registry/recovery/dispatch/maintenance. |
| `internal/app/diagnostics.go` | Baru | Pindahkan Handler, health response, metrics handler dan HTTP server lifecycle existing. Listener hanya dimulai CLI; GUI membaca status lewat controller. |
| `internal/app/systemprompt.txt` | Pindah | Pindahkan isi persis dari `cmd/wazzapagent/systemprompt.txt`; satu sumber prompt untuk CLI dan GUI. Jangan mengubah policy text dalam refactor ini. |
| `internal/app/system_policy.go` | Baru | Embed/render policy; dependency waktu/nama eksplisit. Hapus `SetSystemPolicy` dan mutable package global, bukan menambah wrapper compatibility. |
| `internal/app/app_test.go`, `runtime_test.go`, `system_policy_test.go` | Ubah/baru | Health CLI tetap benar, GUI runtime tidak bind HTTP, constructor failure menutup resource, restart berulang, cancellation, dan literal prompt placeholders tidak berubah. |
| `internal/account/runtime.go`, `runtime_test.go` | Ubah | Integrasikan progress pairing dan state terbaru lewat satu pemilik event; Closed channel tidak boleh membuat busy loop. Pertahankan Ready hanya untuk koneksi valid. |

Batas P1: ekstraksi lifecycle, dependency policy/pairing, shutdown, diagnostics,
dan penanganan channel akun. Implementasi session-only dan event progres pairing
masuk P3 bersama perubahan adapter; P1 tidak membuat mode palsu atau handler dummy.
Controller settings dan form persistence sudah tersedia pada P2; operasi runtime
UI masih menunggu P3/P4. Untuk pemanggil runtime bersama,
gunakan `app.New(cfg, logger, app.Options{SystemPolicy: ..., Pairing: ...})`.
`Run(ctx)` tidak membuka listener; hanya CLI yang memanggil `RunCLI(ctx)`.
Restart wajib membuat instance baru setelah `Close(ctx)` instance lama selesai.

Bot-enabled toggle tetap melalui gate yang ada ketika bot mode dibuat. Jangan
memperluas scope ini menjadi refactor command registry, permission expression,
history rendering, atau provider fallback yang tidak diperlukan untuk aplikasi.

### 4.3 Konfigurasi dan storage

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `internal/config/settings.go` | Baru | Typed Settings, defaults, draft/session/Agent validation, readiness issues, dan konversi ke runtime Snapshot. Tidak melakukan I/O. |
| `internal/config/fields.go` | Baru | Katalog semua key bagian 3.2, group, batas nilai, sensitivitas, dan apply scope. Aturan validasi otoritatif tetap satu implementasi Go. |
| `internal/config/config.go` | Ubah | Loader CLI memetakan env ke Settings lalu validasi/snapshot. Pisahkan parsing/bootstrap root dari `LoadRuntime` yang sekarang membuat identity, supaya lease dapat diperoleh lebih dahulu. Pisahkan `validateEnabled`; pertahankan accessor runtime yang masih dipakai core. |
| `internal/config/dotenv.go` | Ubah | Hapus `go:embed embedded.env` dan fallback embedded; expose parser file/content bounded untuk import tanpa akses process-global. |
| `internal/config/dotenv_export.go` | Baru | Serializer key canonical dengan quoting/escape yang dapat dibaca parser sendiri; secret exclusion dan read-only key exclusion eksplisit. |
| `internal/config/embedded.env.example` | Hapus saat fallback dihapus | Contoh build-time secret tidak lagi menjadi workflow. Jangan membaca/menghapus file secret lokal `embedded.env` pengguna; cukup pastikan tidak direferensikan build. |
| `.env.example` | Ubah | Sinkronkan key, documented defaults vs tuning, CLI precedence, dan petunjuk impor UI. Tambahkan toggle runtime yang belum tercantum. |
| `internal/config/runtime_identity.go` | Ubah | Buka/resolve identity setelah lease diperoleh; API untuk rotasi scope eksplisit pada logout/pairing replacement, dengan write atomic dan recovery yang terdefinisi. Jangan regenerate saat file corrupt. |
| `internal/adapters/sqlite/settings.go` | Baru | OpenSettings/SettingsStore terpisah dari conversation Store; transaksi CAS, decode/validate, Close/checkpoint; secret tidak diekspos melalui DTO. |
| `internal/adapters/sqlite/session_binding.go`, `session_binding_test.go` | Baru | Repository state binding/rotasi pada settings DB; transaksi per transisi, recovery pending rotation, account mismatch guard. Tidak menyimpan auth material Hypermeow. |
| `internal/adapters/sqlite/settings_migrations/001_settings.sql` | Baru | Singleton settings row dan tabel binding sesi yang dipisahkan dari settings revision, termasuk state untuk pergantian scope. Ledger khusus settings DB. |
| `internal/adapters/sqlite/migrations.go` | Baru hasil ekstraksi | Ekstrak runner checksummed existing agar dapat menerima FS/subdirectory untuk dua DB. Pertahankan checksum dan isi migration conversation existing. Tidak membuat migration framework baru. |
| `internal/adapters/sqlite/store.go` | Ubah terbatas | Panggil runner hasil ekstraksi dengan migration conversation; jangan menjadikan settings DB sebagai conversation Store. |
| `internal/adapters/sqlite/config.go` | Ubah | Tambahkan rekonsiliasi projection global dalam satu transaksi per account; compare sebelum update/version bump; preserve override dan moderation. |
| `internal/adapters/sqlite/inbound.go` | Pertahankan/ubah minimal | Pakai `ReconcileAccountPolicy` existing sebelum bot start; jangan ubah semantik wildcard allowlist. |
| `internal/config/settings_test.go`, `dotenv_test.go` | Baru | Blank setup, validasi mode, tipe/range, secret patch, env precedence, export/import escaping, seluruh key terpetakan. |
| `internal/config/config_test.go` | Ubah | Sesuaikan pemisahan bootstrap/identity; pertahankan test stable identity, corrupt identity tidak di-reset, explicit env file wajib ada, precedence, dan backup yang tidak memerlukan LLM valid. Semua fixture menggunakan nilai sintetis. |
| `internal/adapters/sqlite/settings_test.go`, `config_test.go` | Baru | Reopen persistence, CAS conflict, invalid payload, migration checksum, idempotent reconcile, preserve per-chat override/level. |

Jangan mengubah migration `001_part1.sql` sampai `011_message_mentions.sql` yang
sudah ada. Jika implementation membutuhkan schema app tambahan, buat migration
bernomor berikutnya dan jelaskan kebutuhan; desain projection di atas dapat
memakai kolom existing tanpa menambah tabel journal apply.

CLI juga menggunakan repository binding sesi pada settings DB untuk memastikan
scope tidak tercampur ketika bergantian dengan GUI. Ini tidak mengubah sumber
konfigurasi CLI menjadi settings GUI; konfigurasi CLI tetap env/file. Rekonsiliasi
global CLI harus memakai snapshot CLI yang sedang aktif, bukan row settings GUI.

### 4.4 Controller dan adapter WhatsApp

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `internal/control/ports.go` | Baru | Port bagian 3.4; interface sempit, compile-time typed. Tidak memakai `any` untuk runtime/repository/client. |
| `internal/control/types.go` | Baru | Status, mode, operation result, revision, pairing snapshot, public settings view, field errors, secret action. Core types tidak memiliki tag/API Wails. |
| `internal/control/controller.go` | Baru | Ownership context, serialisasi lifecycle, snapshot status cepat, bounded event publication, operation ID/generation. CancelPairing/Stop harus bisa membatalkan operasi panjang tanpa menunggu mutex yang ditahannya. |
| `internal/control/settings.go` | Baru | Get/Validate/Save/Apply, secret merge backend, expected revision, algoritme bagian 3.5. |
| `internal/control/session.go` | Baru | Start/Stop/Reconnect/BeginPairing/CancelPairing/Logout, scope binding dan mismatch guard, validasi sesuai mode. |
| `internal/control/env.go` | Baru | Preview/commit import bounded + token sementara + CAS; export explicit secret selection. Tidak membuat API arbitrary file read. |
| `internal/control/data.go` | Baru pada tahap data | Orkestrasi offline backup/restore/adopt/change data root, resource close/reopen dan status progres. |
| `internal/control/controller_test.go`, `settings_test.go`, `session_test.go` | Baru | Fake runtime/repository; race start/stop/apply, stop saat pairing, save revision stale, apply failure, logout timeout, event lama, crash boundaries. |
| `internal/adapters/whatsapp/hypermeow/adapter.go` | Ubah | Tambahkan mode eksplisit dan branch session-only; bot tetap membutuhkan target/owner/allowlist/handler. Extract lifecycle/pairing bila membuat file lebih jelas, tanpa menyalin client. |
| `internal/adapters/whatsapp/hypermeow/session.go` | Baru | SessionPresent/account identity, logout melalui API library, phone pairing, normalized session result tanpa provider credential leakage. |
| `internal/adapters/whatsapp/hypermeow/pairing.go` | Baru | Pindahkan consumePairing/terminal sink, perluas sink typed menjadi progress QR/phone/success/expired/error; update semua implementasi dan fake sekaligus. |
| `internal/adapters/whatsapp/hypermeow/adapter_test.go`, `session_test.go` | Ubah/baru | Session-only tanpa LLM/handler/targets; bot validation tetap ketat; QR sebelum connect; invalidation dan cleanup; mocked logout ordering. |
| `internal/adapters/whatsapp/hypermeow/store.go` | Ubah hanya jika terbukti | Path berasal dari platform resolver. Uji spasi/Unicode; jangan mengganti driver atau URI escaping tanpa test kegagalan yang relevan. |

Raw error provider/pairing yang sekarang mungkin membawa continuation/raw event
tidak boleh diteruskan ke UI. Mapping error memilih code, pesan aman, field, dan
retryable; detail sensitif tidak menjadi debug dump otomatis.

### 4.5 Platform, data root, dan backup

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `internal/platform/paths.go` | Baru | `Paths` typed: bootstrap config directory, default/effective data root. Pastikan absolute/canonical, bukan working directory. |
| `paths_windows.go`, `paths_linux.go`, `paths_darwin.go`, `paths_android.go` dalam package platform | Baru | Resolver per OS; Linux memakai constraint `linux && !android`, macOS `darwin && !ios`. Android menerima internal files dir dari native host, tidak mengarang HOME. |
| `internal/platform/bootstrap.go` | Baru | `bootstrap.json` di lokasi config OS tetap, hanya pointer data root desktop. Atomic write; corrupt -> recovery error. Ini menyelesaikan lingkaran settings DB yang berada di data root yang dapat dipindah. |
| `internal/platform/lock.go`, `lock_windows.go`, `lock_unix.go` | Baru | OS-backed exclusive file lock, lifetime process/controller. PID file biasa bukan lock. Release otomatis saat process exit; canonical path mencegah alias root. Unix constraint mencakup Android yang diuji. |
| `internal/platform/paths_test.go`, `lock_test.go` | Baru | Env/path injected; path spasi/Unicode, fallback XDG, unavailable dir; subprocess test dua pemilik data root. |
| `internal/backup/backup.go`, `backup_test.go` | Ubah terbatas | Backup menyertakan settings, identity, semua tenant DB/binding state. Exclude lock/transient bootstrap operation file. Checksum manifest tidak dihilangkan; restore tidak overwrite data aktif. |
| `internal/control/data_test.go` | Baru | Runtime dan settings DB ditutup sebelum backup; restore rusak ditolak; perubahan root gagal tidak mengganti pointer; data lama tetap utuh. |

Ketentuan data operation:

- GUI memegang data-root lease sejak membuka settings sampai quit. CLI memegang
  lease yang sama. Backup/restore CLI gagal dengan pesan jelas jika GUI aktif.
- Backup GUI: stop runtime, checkpoint/tutup settings DB, copy+verify dengan
  primitive backup existing, lalu reopen settings. Controller tetap hidup dari
  state memori. Jangan copy file SQLite yang masih terbuka dan mengandalkan `.db`
  saja; jangan hilangkan WAL sebelum checkpoint selesai.
- Restore/adopt: verifikasi sumber, pilih destination baru/empty, hentikan
  pemilik lama, salin dan cek integrity/schema, baru pindahkan bootstrap pointer.
  Backup tidak mengandung absolute path root sumber sebagai aturan restore.
- ChangeDataRoot hanya desktop: lock destination, checkpoint/tutup semua store,
  copy+verify, atomically switch pointer, reopen; source tetap tersedia sampai
  hasil terverifikasi. Jangan melakukan recursive move/delete otomatis.
- Android memakai internal storage tetap. File picker Storage Access Framework
  bisa mengembalikan URI, bukan filesystem path; native adapter melakukan stream
  bounded/staging internal. Jangan pass `content://` ke `os.Open`.
- Ekspor backup Android menggunakan archive terdefinisi yang membungkus manifest
  backup existing; cegah path traversal saat extract. Backup termasuk secret/sesi,
  sehingga UI menjelaskan isinya dan tidak mengunggah/membagikannya otomatis.
- Crash recovery setelah rotasi scope: buat scope baru dan pending marker dulu,
  tulis identity atomic, finalisasi active binding. Startup menyelesaikan marker
  yang konsisten atau berhenti pada recovery screen; tidak menghapus scope lama
  dan tidak pernah menjalankan outbox sebelum identity/device cocok.
- Factory reset/hapus seluruh data tidak diimplementasikan pada increment awal.

### 4.6 Adapter Wails dan frontend

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `internal/adapters/wails/service.go` | Baru, `gui` | Register service publik yang membungkus controller; hanya method pada tabel API, tanpa expose lifecycle internal/repository. |
| `internal/adapters/wails/dto.go` | Baru, `gui` | Mapping DTO serializable; revision string, duration string, public secret status, structured safe errors. Jangan expose config Snapshot secara langsung. |
| `internal/adapters/wails/events.go` | Baru, `gui` | EventSink ke Wails runtime, lifetime subscribe/unsubscribe, snapshot recovery, event generation check. |
| `internal/adapters/wails/files.go` | Baru, `gui` | Native file chooser/import/export/backup stream. Path dari pilihan pengguna, bukan string arbitrary yang diterima service public. |
| `internal/adapters/wails/service_test.go` | Baru, `gui` | Mapping secret/error, batas expose method, event payload; core controller tests tidak memerlukan WebView. |
| `frontend/package.json`, `package-lock.json` | Baru dari template | React, TypeScript, Vite, runtime Wails versi sesuai template, scripts dev/typecheck/build/test. Gunakan npm dan satu lockfile. Tambah QR rendering library hanya jika dibutuhkan; tidak pakai layanan QR eksternal. |
| `frontend/tsconfig.json`, `vite.config.ts`, `index.html` | Baru dari template | TS strict, asset base compatible native shell, viewport/safe area; no localhost proxy untuk production IPC. |
| `frontend/src/main.tsx` | Baru | Mount React, global styles, provider state; jangan memulai koneksi WhatsApp dari render/effect. |
| `frontend/src/App.tsx` | Baru | Empat halaman lewat state navigasi sederhana; belum perlu router dependency. Setup/recovery view berdasarkan status backend. |
| `frontend/src/services/backend.ts` | Baru | Wrapper typed generated bindings; satu tempat memanggil service. Jangan implementasikan HTTP adapter kosong atau fallback mock. |
| `frontend/src/hooks/AppProvider.tsx`, `useRuntime.ts`, `useSettings.ts` | Baru | Context status dan draft, subscribe/read snapshot/reconcile sequence, field errors, operasi pending, unsubscribe. React StrictMode tidak boleh menggandakan Start/Pairing. |
| `frontend/src/layouts/AppLayout.tsx` | Baru | Sidebar desktop, bottom navigation mobile, safe area, scroll/keyboard tanpa menutup action penting. |
| `frontend/src/pages/OverviewPage.tsx` | Baru | WhatsApp state dan Agent state terpisah, Start/Stop, saved vs active revision, last error aman. |
| `frontend/src/pages/WhatsAppPage.tsx` | Baru | Account identity, QR/kode, expiry, cancel/retry/reconnect/logout, konfirmasi logout sebagai destructive action. |
| `frontend/src/pages/SettingsPage.tsx` | Baru | Semua kategori field, advanced sections, secret keep/replace/clear, validate/save/apply, import/export. Tidak mengaktifkan runtime saat Save. |
| `frontend/src/pages/AppDataPage.tsx` | Baru | Versi/platform, lokasi data, backup/restore/change-root sesuai capability dan tahap implementasi. Tindakan belum tersedia diberi status yang jelas. |
| `frontend/src/components/SettingsField.tsx`, `SecretField.tsx` | Baru | Form typed dari metadata; key canonical dapat dilihat di advanced help; tampilkan per-field error tanpa secret echo. |
| `frontend/src/components/PairingPanel.tsx`, `StatusBadge.tsx`, `ConfirmDialog.tsx` | Baru | QR lokal, text code/expiry, status dari backend, dialog konfirmasi tindakan logout/restore. |
| `frontend/src/styles/app.css` | Baru | Layout responsive, focus/keyboard, touch target, safe-area inset, reduced motion; bukan frontend kedua per platform. |
| `frontend/bindings/**` | Generate | Hanya melalui generator versi terpilih. Track output; CI regenerate lalu menolak diff yang belum committed. |
| Test di dekat hook/component terkait | Baru seperlunya | Secret unchanged tidak terkirim sebagai clear; Save tidak Start; event lama diabaikan; QR habis dihapus; form conflict mempertahankan draft. |

Jangan menyalin DTO Go ke TypeScript secara manual jika generator sudah
menghasilkannya. Test frontend fokus perilaku berisiko di atas, bukan snapshot
markup tiap komponen. Tidak menambah dashboard chat, editor history, scheduler,
Redux, atau model provider discovery yang belum diminta.

### 4.7 Native packaging dan CI

| File | Aksi | Isi/perubahan konkret |
| --- | --- | --- |
| `build/windows/Taskfile.yml`, manifest/icon/installer dari template | Baru | WebView2 requirement, metadata produk, build tag `gui`; data tidak diletakkan di install directory. |
| `build/linux/Taskfile.yml`, desktop/package files dari template | Baru | Native GTK/WebKit dependency sesuai Wails yang dipin; task build/package dan app data sesuai XDG. |
| `build/darwin/Taskfile.yml`, `Info.plist`, `Info.dev.plist` | Baru | App bundle metadata, deployment target resmi, signing via environment/CI secret; data di Application Support. |
| `build/android/Taskfile.yml` dan native files hasil template | Baru | Application ID stabil, min/target SDK, SDK/NDK/JDK dan ABI eksplisit, internal files dir, internet permission, WebView asset loader. Catat nama path native hasil generator di README setelah scaffold. |
| Android Activity/host hasil template | Ubah saat diperlukan | Lifecycle resume/stop, file stream, shared Go runtime; tidak menciptakan client WhatsApp kedua saat Activity recreate. Nama file mengikuti versi generator, bukan tebakan. |
| Android service/manifest files hasil template | Baru/ubah pada tahap background | Foreground service + notification dan lifecycle ownership yang telah diuji; pilih service type berdasarkan aturan Android yang berlaku. Jika tidak layak, laporkan batas dukungan. |
| `.github/workflows/ci.yml` | Ubah | Pertahankan core generation/test/vet/race. CLI cross-build memakai target eksplisit `./cmd/wazzapagent`, bukan semua cmd termasuk GUI. Directory `cmd/whatsapp-capability` saat audit tidak memiliki source Go tracked; jangan jadikan target build. |
| `.github/workflows/app.yml` | Baru | npm ci/typecheck/test/build, binding regen/diff, GUI Go tests dengan tag gui, native build Windows/Linux/macOS dan Android APK sesuai toolchain. Jangan menyebut go cross-build sebagai pengujian native UI. |
| `README.md` dan README folder scaffold | Ubah setelah fitur tersedia | Perbarui status/command nyata, prerequisites, lokasi data, semantics Save/Apply/Stop/Logout, batas background Android; hapus klaim placeholder yang sudah usang. |

File dengan dependency Wails dan `frontend/assets.go` seluruhnya memakai `gui`
supaya `go test ./...` core tidak memerlukan `frontend/dist`. Native tasks/CI
menjalankan frontend build sebelum compile/embed dan meneruskan tag gui pada
binding generation juga. Jangan menimpa tag resmi seperti production/Android
dengan tag gui; keduanya diperlukan sesuai template. Jangan membuat dist palsu
atau checked-in empty HTML untuk melewati kegagalan asset embedding.

Gunakan product name `WazzapAgent` dan application/bundle ID baru
`io.github.chomosuke9.wazzapagent`, kecuali ditemukan ID aplikasi existing yang
harus dipertahankan. Release signing tetap input lokal/CI secret. Native template
path final dicatat setelah P0. Jangan mengganti application ID pada update karena
dapat memisahkan sandbox data Android dari instalasi sebelumnya.

## 5. Paket kerja dan acceptance gate

Tabel ini adalah peta kerja; P0, P1, dan sebagian P2/P5 sudah memiliki hasil
lokal yang dicatat pada bagian 7-9. Satu paket boleh terdiri dari beberapa
perubahan kecil; setiap perubahan harus compile pada jalur yang disentuh.
Jangan centang gate hanya karena folder/file sudah ada.

| Paket | Prasyarat | File utama | Hasil dan gate wajib |
| --- | --- | --- | --- |
| P0 - Scaffold resmi dan spike platform | Struktur saat ini | go.mod, Taskfile, cmd/app, frontend minimal/assets, build, app CI awal | Pin Wails/Node/npm; satu binding info app dan event berjalan di Windows; SQLite+Hypermeow compile/load di APK Android. Catat actual native paths, toolchain, ABI, dan blocker. Tidak memasukkan credential/sesi asli. |
| P1 - Lifecycle shared core | P0 untuk GUI hookup | app/runtime/compose/diagnostics/system_policy, CLI, account | CLI health tetap bekerja; GUI tidak bind port; start/stop berulang dan constructor failure bersih; tanpa global mutable policy. |
| P2 - Settings dan app-data | P1 | config, sqlite settings/migrations, platform paths/lock, control settings, Wails settings form | Fondasi lokal lulus: first launch tanpa .env/LLM, save/reopen persistent, CAS conflict, secret masking, bootstrap pointer, lease, dan UI form. Runtime/session belum. |
| P3 - Session management | P1, P2 | control session/controller, Hypermeow adapter/session/pairing, Wails event | Pairing tanpa owner/LLM, QR expiry/cancel, phone code, resume sesi setelah reopen, logout nyata, session-only tidak intake/send. Jalur scope baru tidak menjalankan outbox akun lama. |
| P4 - Apply dan rekonsiliasi | P2, P3 | control settings, sqlite config/inbound, runtime factory | Selesai lokal: model/prompt direkonsiliasi pada chat existing; PromptOverride/moderation tetap; Apply hanya saat runtime aktif; Save bukan Apply; start eksplisit. |
| P5 - UI lengkap | P2-P4 | Wails DTO/service, frontend pages/hooks/components | Runtime status, Start/Stop, settings Apply, dan status koneksi UI sudah tersedia; mobile usability dan data operations tetap tersisa. |
| P6 - Data operations | P2-P5 | platform bootstrap, control data/env, backup, AppData page/native file bridge | Import/export round-trip; backup/restore offline semua DB; adopsi data Go sekarang; move-root crash-safe; Android file URI/stream diuji. |
| P7 - Desktop packaging | P5-P6 | build windows/linux/darwin, app CI/docs | Install, reopen, upgrade dengan data tetap pada masing-masing OS; native dependency/signer requirement didokumentasikan. |
| P8 - Android lifecycle/background | P3-P6 dan spike Android P0 | Android native host/service, platform, responsive UI | Perangkat nyata: activity recreate, force-stop/reopen, upgrade, network switch, screen-off/background; persistence dan always-running dilaporkan terpisah. |
| P9 - Final verification/docs | P7, P8 | Tests, CI, README/status | Matrix kemampuan berisi bukti build/runtime per OS; canary dedicated WhatsApp memverifikasi pesan masuk, reply, dan restart recovery. |

P0 Android tidak boleh sengaja ditunda sampai semua UI selesai. Bila emulator/
perangkat/toolchain tidak tersedia, catat gate belum teruji; pekerjaan core atau
desktop yang independen boleh berjalan, tetapi status multiplatform belum selesai.
Mode web dan iOS tidak termasuk P0-P9.

### 5.0 Catatan toolchain yang wajib diisi pada P0

| Item | Nilai/status sekarang | Yang dicatat pelaksana |
| --- | --- | --- |
| Go | Minimum `1.26.5`; lokal `1.27.0 windows/amd64` | Minimum existing dipertahankan; CI mengikuti go.mod |
| Wails CLI + Go module | `v3.0.0-beta.23`, commit `7556ab76d3c939dce95f1afdf571704175a28702` | CLI/module dipin sama; template dan API diperiksa dari source versi ini |
| Runtime frontend Wails | `@wailsio/runtime 3.0.0-beta.23` | Sesuai source/template Go yang dipin |
| Node/npm | Node `24.19.0`, npm `11.6.2` | `frontend/package-lock.json`; CI memakai versi yang sama |
| React/TypeScript/Vite | React `19.1.1`, TS `5.9.2`, Vite `8.2.0` | Plugin React `6.0.1`; Vitest `4.1.11` |
| Android SDK/NDK/JDK | Belum terpasang pada host Windows yang diperiksa | Template beta.23: API/target 35, minSdk 21, NDK `26.3.11579264`; native cgo belum diuji |
| Linux WebView dependencies | Belum diverifikasi | Distro/versi dan paket GTK/WebKit yang benar-benar dipakai |
| Native template paths | Native Android belum dihasilkan | Target Gradle `build/android`; Manifest/Activity final wajib dicatat saat gate Android dikerjakan |
| Command build/run | `wails3 task package`, `wails3 task run` dari root | Windows executable diuji; Linux/macOS belum diuji; Android task menolak build sampai gate native dipenuhi |

Jangan mengisi tabel ini dengan perkiraan lalu menyatakannya terverifikasi.

### 5.1 Verifikasi yang harus dijalankan pelaksana

Untuk perubahan Go core, gunakan unit/integration tests yang relevan lalu gate
repository. Tidak perlu menjalankan test aplikasi untuk edit dokumentasi saja.

```text
go generate ./...
go test -count=1 ./...
go vet ./...
go build ./cmd/wazzapagent
git diff --check
```

Setelah frontend tersedia, dari direktori frontend:

```text
npm ci
npm run typecheck
npm test -- --run
npm run build
```

P0 harus menyediakan script di atas; gunakan test runner ringan (misalnya Vitest)
untuk kasus perilaku yang diwajibkan. Binding generation lalu diff check dilakukan
sesuai task aktual. GUI Go checks memakai `-tags gui` dengan dependencies native
dan bundle tersedia. Race tests difokuskan ke controller/runtime/store pada runner
yang mendukung; CI race core existing tetap dipertahankan.

Perintah Windows aktual tersedia pada bagian 7 dan `build/README.md`. Perintah
Linux/macOS masih memerlukan verifikasi host native; Android memiliki gate eksplisit.
Saat mengubah command descriptor, generated registry harus diperbarui; pekerjaan
aplikasi sendiri tidak memerlukan perubahan descriptor.

### 5.2 Skenario penerimaan produk

| Skenario | Hasil yang harus terlihat |
| --- | --- |
| Instalasi kosong, tanpa env, tanpa jaringan | Window/setup terbuka; tidak crash karena LLM key kosong |
| Save sebagian setup lalu restart | Draft persistent; alasan Agent belum siap ditampilkan |
| Save API key lalu buka settings | Hanya configured status; tidak ada key di hasil GetSettings/log |
| Pair dan buka ulang aplikasi | Device session tetap tersedia; tidak diminta QR lagi jika masih valid |
| Pairing dibatalkan/expired lalu event lama datang | Tidak ada QR lama kembali; tidak ada client tambahan |
| Start/Stop ditekan berulang saat operasi berjalan | Satu owner runtime, status busy/idempotent yang konsisten |
| WhatsApp open tetapi Agent disabled | UI membedakan keduanya; session-only tidak memproses pesan |
| Provider/model/prompt diubah | Saved revision berubah; active tetap sampai Apply; chat existing memakai konfigurasi baru sesudah Apply |
| Prompt override/moderation sudah ada | Apply global tidak menghapus override atau level chat |
| Allowlist dipersempit | Intake dan outbound recovery mengikuti policy baru sebelum runtime aktif |
| Crash setelah app DB reconcile, sebelum runtime start | Start berikutnya mengulang rekonsiliasi tanpa double mutation/delivery |
| Shutdown timeout | Tidak ada runtime baru berjalan bersamaan dengan worker lama |
| Sesi dicabut lewat WhatsApp | UI meminta pairing lagi; data lama tidak dihapus; scope baru aman |
| Logout lalu pair akun lain | Riwayat/outbox lama tidak dipakai oleh akun baru |
| GUI dan CLI memakai root sama | Pemilik kedua ditolak sebelum membuka store/identity |
| Backup/restore/move gagal di tengah | Source tetap utuh; root aktif tidak beralih ke copy tidak lengkap |
| Data store corrupt | Recovery error; tidak reset atau create-empty otomatis |
| Install update tiap OS | Settings/session sama selama app ID/path data dipertahankan |
| Android Activity recreate/force-stop/reopen | Tidak ada runtime duplikat; data tetap; koneksi dibangun ulang sesuai state nyata |

## 6. Format handoff untuk model pelaksana

Contoh instruksi yang dapat diberikan ke model berikutnya:

> Kerjakan hanya P2 pada docs/multiplatform/README.md setelah memverifikasi P1.
> Ikuti keputusan D01-D13 dan kontrak settings. Baca current code sebelum edit.
> Pertahankan perubahan pengguna. Laporkan file yang diubah, gate yang lulus,
> yang belum teruji, dan blocker dengan bukti. Jangan menjalankan akun/data asli.

Laporan akhir setiap paket wajib berisi:

```text
Paket: Pn
Status: selesai / parsial / terblokir
Prerequisite yang diverifikasi:
File dibuat/diubah/dipindahkan:
Perilaku yang sekarang bekerja:
Command pemeriksaan dan hasil:
Native/device evidence (atau belum diuji):
Data/schema/kontrak yang berubah:
Penyimpangan dari dokumen dan alasannya:
Gate tersisa serta paket berikutnya:
```

Setelah selesai, perbarui status paket dan command/toolchain aktual di dokumen ini.
Jangan menandai P3/P9 selesai dari mock tests saja. Jika ada blocker arsitektur yang
memerlukan keputusan baru, jelaskan simbol/file, bukti error, dan pilihan konkret;
jangan membangun fallback tersembunyi untuk menyamarkan masalahnya.

## 7. Hasil implementasi P0 (21 September 2026)

**Status: parsial.** Gate shell Windows lulus; gate APK/SQLite load Android belum
lulus. Implementasi dikerjakan tiga subagent GPT Luna, kemudian diintegrasikan
dan diperiksa oleh orchestrator. Ini laporan gate P0; kelanjutan P1 dan fondasi
P2 dicatat pada bagian 8 dan 9. Controller dan paket P3-P9 masih tersisa.

### File yang sudah diisi

| File/area | Isi aktual | Perubahan berikutnya |
| --- | --- | --- |
| `cmd/app/main.go` | Wails window, typed event registration, AppService, embedded assets; tag `gui`; resolve paths, lease data root, buka settings store, register controller | P3 menambah runtime/session owner; jangan start runtime saat constructor |
| `internal/adapters/wails/service.go` | DTO AppInfo/PingEvent dan settings; GetAppInfo, Ping -> `app:ping`, Get/Validate/SaveSettings | P3-P5 menambah API session/apply/data sesuai controller |
| `frontend/assets.go` | Embed `all:dist`, hanya pada build GUI | Tetap gunakan keluaran Vite, tanpa secret |
| `frontend/bindings/` | Hasil generator: satu service, enam method, dua belas model, satu event | Regenerate dari `./cmd/app`; jangan edit manual |
| `frontend/src/` | Empat halaman, navigasi responsive, AppProvider, event subscription/cleanup, form settings backend-backed tanpa secret | P3/P5 menambah status sesi, pairing, apply, dan data operations |
| `frontend/package*.json`, `tsconfig.json`, `vite.config.ts`, `vitest.config.ts` | Versi dependency dipin, npm lock, typecheck/test/build | Pertahankan lock dan jalankan audit saat mengganti dependency |
| `Taskfile.yml`, `build/**/Taskfile.yml`, `build/config.yml` | Build desktop, dev config, gate Android eksplisit | P7 packaging native; Android masih perlu generated host dan toolchain |
| `.github/workflows/app.yml` | Job Windows: frontend checks, binding drift, GUI compile, artifact executable | Workflow ditulis tetapi belum dijalankan di GitHub Actions |
| `.github/workflows/ci.yml` | Build core diarahkan eksplisit ke `./cmd/wazzapagent` | Pertahankan core tests/race/cross-build tanpa dependency GUI |
| `internal/control/`, `cmd/server/` | Controller settings P2 tersedia; runtime/session port masih reservasi | Jangan menganggap UI memiliki operasi runtime/session |
| `internal/platform/` | Canonical data-root, OS-backed lease, fixed config directory, dan atomic `bootstrap.json` pointer | P6 menambah backup/restore/move-root operations; lease sudah dipasang pada CLI/GUI bootstrap |

### Build dan verifikasi aktual

Dari root, setelah Go, Node `24.19.0`, npm `11.6.2`, dan Go binary directory
tersedia pada PATH:

```text
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
wails3 task package
wails3 task run
```

Output Windows: `bin/WazzapAgent.exe`. `package` saat ini membuat executable
production, belum installer/signing. Untuk development gunakan `wails3 task dev`;
alur hot reload belum diuji interaktif. Browser Vite biasa tidak menyediakan
bridge Go; verifikasi method/event dilakukan di window Wails.

Host pengujian memiliki Node tetapi tidak memiliki npm pada PATH. Pemeriksaan
frontend memakai npm `11.6.2` sementara melalui pnpm. `wails3 task package`
memiliki precondition npm pada PATH yang belum terpenuhi pada host ini; asset
dan executable tetap dibangun melalui langkah frontend langsung dan `go build`.
Tidak ada absolute path komputer pengembang yang ditulis ke konfigurasi build.

| Pemeriksaan | Hasil |
| --- | --- |
| `npm ci`, `npm run typecheck`, `npm test -- --run`, `npm run build` | Lulus; 2 test parsing payload/event unsubscribe |
| `npm audit` | 0 vulnerability setelah Vitest diperbarui ke `4.1.11` |
| Generator `wails3 generate bindings -f '-tags gui' -clean=true -ts -i ./cmd/app` | Lulus; binding menyertakan event bertipe, tanpa DTO manual frontend |
| `go test -tags gui ./...`, `go vet -tags gui ./...`, `go test ./...`, `go vet ./...` | Lulus di Windows |
| `go build ./cmd/wazzapagent` | Lulus; CLI tidak menjalankan runtime saat build |
| `wails3 task package` | Terblokir di precondition npm PATH host; `frontend` build dan `go build -tags gui,production ... ./cmd/app` lulus |
| Native Windows/WebView2 | Window dibuka dari working directory sementara, GetAppInfo menampilkan `windows / dev`, tombol ping menampilkan `pong #1` melalui event Go |
| `git diff --check` | Lulus |

Pengujian tidak memakai `.env`, kredensial provider, database, atau sesi WhatsApp
asli. Keberhasilan ping tidak membuktikan sesi/persistence; P0 tidak memiliki
operasi save, pairing, logout, restore, atau runtime start.

### Gate Android dan platform lain

- SQLite, Hypermeow, dan CLI berhasil dikompilasi untuk `android/arm64` dengan
  `CGO_ENABLED=0`. Ini hanya membuktikan kompilasi Go; belum membuktikan library
  dapat dimuat oleh APK atau database/session bertahan di perangkat.
- Wails APK memakai `CGO_ENABLED=1` dan `-buildmode=c-shared`. Host yang diperiksa
  belum memiliki SDK/NDK/JDK/ADB/emulator; kompilasi CGO tanpa NDK gagal pada
  header/assembler host. Template beta.23 memilih NDK host Darwin/Linux dan
  belum memiliki cabang Windows.
- `wails3 task android:build` gagal dengan pesan native project belum tersedia.
  Setelah file native dihasilkan pun task masih memiliki gate eksplisit sampai
  implementasi build diverifikasi; file Gradle saja tidak berarti APK siap.
- Pelaksana Android berikutnya memakai host Linux/macOS dengan toolchain sesuai
  source beta.23, mengadaptasi entrypoint `./cmd/app`, menghasilkan native host,
  lalu membuktikan SQLite open/write/reopen dan Hypermeow load dalam APK.
  Catat lokasi Manifest/Activity/JNI, ABI, API device, dan build command aktual.
- Jangan menyalin stock deploy yang memanggil `adb uninstall`; itu menghapus
  data. Verifikasi upgrade dengan install yang mempertahankan data dan app ID.
- Task Linux/macOS tersedia tetapi belum dijalankan pada host tersebut. Layout
  responsive tersedia tetapi keyboard/safe-area/device Android belum diverifikasi.

Kelanjutan P0 adalah lifecycle shared core P1 (hasil pada bagian 8). Pekerjaan
core/settings yang independen dapat berjalan sambil toolchain Android disiapkan,
tetapi status multiplatform tetap parsial sampai gate native lulus.

## 8. Hasil implementasi P1 (21 September 2026)

**Status: selesai secara lokal.** Implementasi dibagi kepada tiga subagent Luna,
diintegrasikan dan diperbaiki melalui review orchestrator. P1 memisahkan lifecycle
core dari diagnostik CLI, tanpa mengaktifkan koneksi WhatsApp melalui UI P0.

### Kontrak implementasi untuk paket berikutnya

- `app.New(cfg, logger, app.Options{SystemPolicy, Pairing})` membuat satu pemilik
  runtime. Constructor belum membuka koneksi WhatsApp atau listener.
- `Run(ctx)` menjalankan core tanpa HTTP. `RunCLI(ctx)` menambahkan endpoint
  `/health/live`, `/health/ready`, dan `/metrics` untuk CLI.
- `Run` menunggu worker dan cleanup benar-benar selesai. `RunCLI` membatasi waktu
  tunggu shutdown dengan `ShutdownTimeout`; jika timeout, goroutine pemilik tetap
  menyelesaikan cleanup dan instance tersebut tetap tidak dapat dijalankan ulang.
  CLI akan keluar dengan error. Jangan menganggap timeout sebagai logout.
- `Close(ctx)` membatalkan runtime dan menunggu penghentian worker serta cleanup.
  Timeout caller tidak memberi izin memulai instance pengganti pada data root
  yang sama. Tunggu cleanup instance lama; jangan memperlakukan error timeout
  sebagai sukses Stop. Instance yang sudah dijalankan tidak dipakai ulang.
- Policy berasal dari `internal/app/systemprompt.txt` melalui
  `RenderSystemPolicy(assistantName, now)`. CLI menyediakan waktu/nama dan sink
  terminal secara eksplisit. GUI nantinya menyediakan sink pairing miliknya;
  tidak perlu mengambil alih `os.Stdout`.
- Event/fatal channel connector tetap dikonsumsi oleh account runtime. UI
  berikutnya memakai status/event controller, bukan membaca channel connector
  secara paralel. Channel tertutup tidak menyebabkan busy loop.
- `Adapter.Stop` memiliki satu proses cleanup bersama. Timeout tidak menutup
  device store yang masih dimiliki worker. Operasi keluar juga ditolak setelah
  shutdown mulai, termasuk jika event Connected terlambat datang.

| Area | Perubahan aktual | Pekerjaan berikutnya |
| --- | --- | --- |
| `internal/app/app.go`, `runtime.go`, `diagnostics.go` | Ownership Run/Close, core tanpa HTTP, CLI diagnostics, cleanup worker | Controller P2/P3 mengelola operasi dan status UI; jangan menambah owner runtime kedua |
| `internal/app/compose.go` | Composition bot existing dipisahkan, policy/pairing di-inject, cleanup kegagalan constructor | P3 menambah branch session-only tanpa pipeline Agent |
| `internal/app/system_policy.go`, `systemprompt.txt` | Satu embedded policy bersama, placeholder spesifik, tanpa setter global | Konten policy tidak berubah pada P1 |
| `cmd/wazzapagent/main.go` | Explicit Options, terminal pairing, RunCLI | P2 mengganti loader settings/identity sesuai desain |
| `internal/account/runtime.go` | Readiness valid, channel tertutup, propagasi error shutdown | P3 menambah progres pairing terstruktur |
| `internal/adapters/whatsapp/hypermeow/adapter.go` | Stop idempotent dengan completion bersama; tunggu worker sebelum tutup device store | P3 session lifecycle resmi; tanpa protokol WhatsApp buatan |

### Bukti verifikasi

| Pemeriksaan | Hasil |
| --- | --- |
| `go generate ./...` dan diff registry | Lulus; generated command registry tidak berubah |
| `go test -count=1 ./...` | Lulus seluruh package |
| `go vet ./...` | Lulus |
| `go test -race -count=1 ./internal/app ./internal/account ./internal/adapters/whatsapp/hypermeow` | Lulus; package app diulang setelah perbaikan integrasi terakhir |
| Build `./cmd/wazzapagent` | Lulus; output lokal `bin/wazzapagent-headless.exe` |
| Build `-tags gui,production ./cmd/app` | Lulus; output lokal `bin/WazzapAgent.exe`; UI tidak berubah pada P1 |
| Prompt lama vs sumber baru | Git blob identik `7ce27f73330488cd6ecb0d611feeed2894b96abf`; placeholder lain tidak diubah |
| `git diff --check` | Lulus |

Tes menjalankan tiga instance CLI baru secara berurutan pada data root sementara,
memanggil ketiga endpoint lewat HTTP loopback, dan memastikan listener ditutup.
Tes lain membuktikan core berjalan saat port diagnostik sudah terpakai, timeout
tidak melepas resource worker, hasil worker `nil` tidak menggantung, start kedua
ditolak tanpa membatalkan owner pertama, pembatalan constructor tidak menyalakan
worker, dan error cleanup tetap diterima lewat `Run`/`Close` berulang.
Kegagalan context builder juga diperiksa agar tidak meninggalkan WAL/SHM terbuka.

Pengujian memakai config sintetis, fake connector/worker, dan database sementara;
tidak memakai akun/sesi WhatsApp asli. Tes HTTP ini menguji integrasi `RunCLI`,
bukan klaim canary WhatsApp. Tidak ada native Android/device test baru pada P1.

### Batas pekerjaan

P1 tidak mengubah skema conversation data, command registry, izin command,
provider fallback, atau isi prompt. P2-P4 menghubungkan UI dengan lifecycle dan
composition runtime yang sudah dipakai CLI, tanpa membuat handler pesan kedua.
Runtime GUI tidak membuka listener HTTP diagnostics. Gate Android P0, uji
pairing/pesan dengan perangkat nyata, dan adopsi data root lama tetap perlu
diverifikasi terpisah.

## 9. Fondasi implementasi P2 (21 September 2026)

**Status: fondasi settings, bootstrap GUI, dan form settings selesai secara lokal;
runtime/session operations belum.** Paket ini menyiapkan bagian yang dapat diuji
tanpa membuka runtime WhatsApp:

- `internal/config/settings.go` menyediakan `Settings` typed, default GUI,
  validasi draft/session/Agent, readiness issues, katalog field, masking secret,
  dan konversi ke `config.Snapshot` tanpa I/O eksternal.
- `internal/config/dotenv.go` tidak lagi meng-embed atau memakai fallback
  `embedded.env`. Parser bounded dapat dipakai untuk import content/file; proses
  environment tetap mengalahkan `.env` default atau file eksplisit.
- `internal/config/dotenv_export.go` mengekspor key secara deterministik dengan
  escaping yang round-trip ke parser. Secret dan identity read-only dikeluarkan
  secara default dan hanya dapat disertakan lewat opsi eksplisit.
- `internal/adapters/sqlite/settings.go` dan
  `settings_migrations/001_settings.sql` membuat `settings.db` terpisah dari
  conversation store, dengan singleton settings/session state, migration ledger
  checksum, payload bound, persistence reopen, dan CAS revision.
- `internal/platform/data_root.go` serta lock OS memakai canonical path dan
  exclusive file lock lintas proses. Alias symlink menuju root yang sama bersaing
  pada lock yang sama; `Close` lease idempotent.
- `internal/control` menyediakan controller settings yang hanya bergantung pada
  typed repository. Save memakai expected revision, serialisasi mutasi, merge
  secret `keep/replace/clear`, dan public view tidak pernah mengembalikan secret.
- `internal/adapters/sqlite/settings_control.go` menjembatani repository SQLite
  ke controller tanpa mengekspos database atau JSON ke core control.
- `internal/platform/paths.go` dan `bootstrap.go` memisahkan config directory
  tetap dari data root yang dapat dipindah, dengan pointer JSON atomic dan
  recovery error untuk pointer rusak. CLI dan GUI memperoleh lease sebelum
  identity/settings store dibuka.
- `internal/adapters/wails/dto.go` dan `service.go` mengekspos typed settings
  melalui generated Wails binding. `frontend/src/pages/SettingsPage.tsx`
  sekarang membaca, mengedit, dan menyimpan seluruh field public settings;
  field secret hanya menerima replace dan dikembalikan sebagai status configured.
  GUI menampilkan effective leased root sebagai read-only; move-root belum
  dianggap sebagai Save.
- `docs/multiplatform/TESTING.md` mendokumentasikan build Windows, persistence
  round-trip, masking secret, data-root lock, dan batas pengujian native.

Bukti lokal untuk fondasi P2:

| Pemeriksaan | Hasil |
| --- | --- |
| `go test -count=1 ./internal/config ./internal/control ./internal/adapters/sqlite ./internal/platform` | Lulus |
| `go test -race -count=1 ./internal/config ./internal/control ./internal/adapters/sqlite ./internal/platform` | Lulus |
| `go vet ./...` | Lulus sebelum dan sesudah fondasi P2 |
| `go test -count=1 ./...` | Lulus setelah perubahan P2 |
| `go test -tags gui ./...` | Lulus; service, DTO, controller, dan GUI bootstrap terkompilasi |
| Frontend `typecheck`, `test -- --run`, `build` | Lulus; 2 test frontend tetap lulus |
| `wails3 generate bindings -f '-tags gui' -clean=true -ts -i ./cmd/app` | Lulus; binding settings digenerate ulang |
| `git diff --check` | Lulus |

Paket ini belum memiliki Apply/Start/session Agent API. CLI masih memakai loader
runtime/env untuk menjalankan mode terminal, sedangkan GUI sudah dapat membuka
settings DB lokal dan menyimpan form dengan CAS revision. P3 session management
dicatat pada bagian 10; Agent runtime tetap tidak aktif sampai P4.

## 10. Hasil implementasi P3: pengelolaan sesi WhatsApp (23 September 2026)

**Status: alur sesi-only selesai secara lokal; pairing sungguhan dengan telepon
belum diverifikasi.** GUI sekarang dapat membuat atau melanjutkan sesi WhatsApp
tanpa memerlukan owner, allowlist, prompt, atau konfigurasi LLM. Sesi ini tidak
memiliki inbound handler maupun operasi mengirim pesan, dan tidak menyalakan
pipeline Agent.

Implementasi utama:

- `internal/control/session_controller.go` memiliki satu pemilik lifecycle untuk
  status, pairing QR/kode telepon, resume, stop, reconnect, cancel, logout, dan
  shutdown. Reservasi pairing yang tertinggal setelah proses berhenti diperiksa
  terhadap device store sebelum operasi baru dimulai.
- `internal/control/session_types.go` dan `ports.go` mendefinisikan kontrak status
  serta runtime sesi terbatas. QR/kode pairing tidak ditulis ke database.
- `internal/adapters/sqlite/settings_session.go` menyimpan scope aktif/pending,
  ID akun WhatsApp, dan state paired/revoked pada `settings.db`, terpisah dari
  revision settings. Logout memakai `Client.Logout` resmi dependency; data chat
  lama tidak dihapus.
- `internal/adapters/whatsapp/hypermeow/session.go` menggunakan `GetQRChannel`,
  `PairPhone`, `Client.Logout`, dan reconnect dari versi Hypermeow yang dipin.
  Client ini tidak menerima handler pesan dan tidak membuka operasi kirim.
- `internal/platform/session_scope.go` meresolusikan scope sesi ke path device
  store yang sama dengan runtime existing. Pairing baru setelah revoke mendapat
  pasangan tenant/account baru agar data sesi lama tidak dipakai oleh akun baru.
- `internal/adapters/wails/service.go`, `dto.go`, dan `session_event.go`
  mengekspos API typed dan event `whatsapp:session`. `cmd/app/main.go` menutup
  runtime sesi sebelum checkpoint DB dan melepas lease data root.
- `frontend/src/pages/WhatsAppPage.tsx` menyediakan QR, kode telepon, cancel,
  resume, stop, reconnect, dan logout. Event dari operasi yang sudah dibatalkan
  diabaikan agar QR lama tidak muncul kembali.

Bukti lokal untuk P3:

| Pemeriksaan | Hasil |
| --- | --- |
| `go test -race ./internal/control ./internal/adapters/sqlite ./internal/adapters/whatsapp/hypermeow ./internal/config ./internal/platform` | Lulus; termasuk cancel/recovery, resume-stop, revoke, logout, rotasi scope, SQLite reopen, dan QR PNG |
| `go test -tags gui ./cmd/app ./internal/adapters/wails ./internal/adapters/whatsapp/hypermeow` | Lulus |
| Generator Wails dengan `-tags gui` | Lulus; TypeScript binding untuk API sesi dan event dibuat ulang |
| Frontend TypeScript check, Vitest, Vite build | Lulus; 4 test event dan payload |
| `git diff --check` | Lulus |

Tes tidak memakai nomor telepon, kredensial WhatsApp, atau perangkat nyata. QR
hasil generator diperiksa sebagai PNG, bukan dipindai; phone-code, pairing
provider, reconnect jaringan, pencabutan jarak jauh, dan logout nyata masih harus
diuji manual dengan akun khusus. Resume menyambungkan sesi tersimpan saat dipilih;
aplikasi tidak membuka WhatsApp otomatis ketika baru diluncurkan.

Implementasi P4 pada bagian 11 menambahkan Apply dan rekonsiliasi settings ke
Agent runtime. Runtime sesi P3 tetap terpisah sampai pengguna menjalankan Agent
secara eksplisit.

## 11. Hasil implementasi P4: runtime Agent dan Apply melalui UI (23 September 2026)

**Status: integrasi runtime dan UI lulus pemeriksaan lokal; percakapan WhatsApp
sungguhan belum diuji pada perangkat.** GUI sekarang menjalankan
`internal/app.Application.Run` yang sama dengan CLI, melalui Wails service dan
controller runtime. Satu data root hanya memiliki satu pemilik client: saat
Agent dijalankan, sesi-only dihentikan; operasi pairing/logout sesi diblokir
sampai Agent berhenti. GUI tidak membuka HTTP listener diagnostics.

- `internal/control/agent_runtime.go` merangkai konfigurasi settings tersimpan,
  revision CAS, binding akun sesi, dan shutdown. Start eksplisit; Apply tidak
  menyalakan Agent yang berhenti, dan Apply pada Agent aktif menghentikan lalu
  membuat ulang runtime dengan revision tersimpan.
- `internal/app/managed_agent.go` memanggil composition runtime existing;
  history, intake inbound, command, Agent, model, dan outbox tetap jalur yang
  sama dengan mode headless.
- `internal/app/compose.go` merekonsiliasi model, batas token, prompt dasar,
  policy, owner, dan allowlist sebelum worker berjalan. Prompt override per-chat
  dan moderasi tidak diubah; reconciliation hanya menaikkan versi row saat nilai
  global berbeda.
- `internal/adapters/wails` menyediakan status runtime, Start, Stop, dan Apply.
  `OverviewPage` menampilkan status Agent/WhatsApp; `SettingsPage` membedakan
  Save dari Apply. Halaman WhatsApp menampilkan saat koneksi dimiliki Agent.
- `cmd/app/main.go` menutup Agent sebelum settings database dan lease dilepas.

Simpan saat runtime berhenti hanya menyimpan revision berikutnya. Save saat
runtime aktif menunjukkan perubahan pending; tombol Apply menjalankan revision
itu. Bila Agent berhenti, pengguna menekan Jalankan Agent di Overview. Start saat
UI dibuka hanya berjalan bila preferensi `startOnLaunch` diaktifkan dan settings
valid serta sesi masih paired; default-nya false dan proses ini tidak memulai
pairing. Settings/revision disimpan di
`settings.db`; conversation dan device database berada pada leased data root
serta tenant/account aktif. GUI tidak mengimpor data dari folder CLI lama secara
otomatis, jadi jalur data lama perlu dipilih/adopsi lewat pekerjaan data
operations sebelum mengklaim database lama dipakai.

Pemeriksaan otomatis mencakup start/stop, owner runtime tunggal, serialisasi
mutasi sesi, Apply revision terbaru, Apply ketika berhenti, validasi pairing dan
settings, preservation prompt override/moderasi, serta idempotensi rekonsiliasi.
Uji balasan nyata tetap memerlukan akun khusus, LLM endpoint/key yang benar,
owner dan allowlist sesuai, serta pesan baru sesudah Agent aktif.

| Pemeriksaan lokal | Hasil |
| --- | --- |
| `go test -race -count=1 ./...` | Lulus |
| `go test -tags gui -count=1 ./...` | Lulus |
| `go vet ./...` dan `go vet -tags gui ./cmd/app ./internal/adapters/wails/...` | Lulus |
| Wails TypeScript bindings (`-tags gui`) | Lulus; 17 methods dan 19 models |
| TypeScript check, Vitest, dan Vite production build | Lulus; 4 test frontend |
| Windows GUI production build | Lulus; `bin/WazzapAgent-P4-test.exe` |
| `git diff --check` | Lulus; hanya peringatan line-ending Git |

## Referensi Wails

- [Struktur scaffold resmi](https://v3.wails.io/getting-started/your-first-app/)
- [Frontend dan generated bindings](https://v3.wails.io/quick-start/first-app/)
- [Mobile overview](https://v3.wails.io/guides/mobile/)
- [Status platform](https://v3.wails.io/status/)
