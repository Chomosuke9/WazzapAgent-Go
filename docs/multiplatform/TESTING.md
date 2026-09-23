# Runbook testing manual

Runbook ini menguji shell Wails, persistence settings, pengelolaan sesi WhatsApp,
runtime Agent yang memakai core headless existing, masking secret, dan ownership
data-root. Gunakan akun WhatsApp khusus untuk pairing/pesan; jangan masukkan API
key produksi.

## Build lokal Windows

Dari PowerShell pada root repository:

```powershell
$ErrorActionPreference = "Stop"
$repo = "C:\Users\bagus\Project\WazzapAgent-Go"
$node = "C:\Users\bagus\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe"
$pnpm = "C:\Users\bagus\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\node_modules\pnpm\bin\pnpm.cjs"

Push-Location "$repo\frontend"
& $node $pnpm dlx npm@11.6.2 ci
& $node $pnpm dlx npm@11.6.2 run typecheck
& $node $pnpm dlx npm@11.6.2 test -- --run
& $node $pnpm dlx npm@11.6.2 run build
Pop-Location

wails3 generate bindings -f "-tags gui" -clean=true -ts -i ./cmd/app
go test -tags gui ./...
go build -tags gui,production -trimpath -ldflags="-s -w -H windowsgui" -o bin/WazzapAgent.exe ./cmd/app
```

Jika Node dan npm sudah tersedia di `PATH`, empat perintah frontend dapat
diganti dengan `npm ci`, `npm run typecheck`, `npm test -- --run`, dan
`npm run build`. `wails3 task package` juga dapat digunakan pada host yang
memenuhi precondition npm.

Jalankan executable:

```powershell
.\bin\WazzapAgent-P4-test.exe
```

## Skenario persistence

1. Buka halaman **Pengaturan**.
2. Ubah nama assistant, owner JID sintetis, allowlist, prompt, endpoint/model,
   satu angka batas, dan satu duration seperti `30s`.
3. Masukkan secret test pada salah satu field API key, lalu tekan **Simpan
   settings**.
4. Pastikan badge berubah menjadi **Tersimpan** dan revision bertambah.
5. Tutup aplikasi dengan normal, jalankan executable lagi, lalu pastikan semua
   nilai non-secret kembali sama.
6. Pastikan input secret kosong dan hanya label **tersimpan** yang terlihat.

`Save` hanya menyimpan settings. Ia tidak memulai atau menghentikan Agent.
Pairing sesi diuji terpisah di bawah.

## Skenario sesi WhatsApp

1. Jalankan executable dan buka halaman **WhatsApp**. Pairing harus bisa dimulai
   walaupun owner, allowlist, prompt, dan API key LLM belum diisi.
2. Pilih **QR code**. Di WhatsApp pada telepon test, buka **Perangkat tertaut**,
   pilih **Tautkan perangkat**, lalu pindai QR yang tampil. Pastikan status
   berubah menjadi **Terhubung**.
3. Kirim pesan test ke akun/perangkat tertaut. Sesi-only tidak boleh membalas
   atau menjalankan Agent.
4. Tutup aplikasi dengan normal, buka lagi, lalu pilih **Sambungkan sesi
   tersimpan**. Pairing QR tidak diminta lagi jika sesi WhatsApp masih valid.
5. Tekan **Hentikan koneksi**, lalu sambungkan lagi. Stop harus mempertahankan
   kredensial lokal.
6. Uji **Kode telepon** dengan nomor format internasional dan masukkan kode
   melalui alur Perangkat tertaut di WhatsApp. Jangan tempelkan nomor/kode ke log
   atau laporan bug.
7. Mulai pairing lalu tekan **Batalkan pairing**. Status harus kembali
   unpaired; QR operasi yang dibatalkan tidak boleh muncul kembali.
8. Untuk uji **Keluar dari WhatsApp**, gunakan akun khusus dan konfirmasi unlink.
   Setelah itu status harus meminta pairing ulang. Pair akun kedua dan pastikan
   aplikasi tetap tidak membalas pesan.

## Skenario Agent UI dan balasan nyata

1. Pastikan akun test sudah paired dan tampil sebagai sesi tersimpan. Pada
   **Pengaturan**, isi nama assistant, owner JID, allowlist chat, prompt, LLM
   endpoint/model, dan API key yang khusus untuk pengujian. Aktifkan mode
   WhatsApp dan Agent, lalu tekan **Simpan settings**.
2. Pada **Overview**, tekan **Jalankan Agent**. Tunggu status Agent berjalan dan
   WhatsApp berstatus client aktif/terhubung. Halaman WhatsApp harus menyatakan
   koneksi sedang dimiliki Agent; operasi pairing/logout tidak boleh tersedia.
3. Dari chat yang masuk allowlist, kirim pesan baru yang meminta jawaban
   sederhana. Pastikan balasan datang dari akun bot, lalu periksa log lokal bila
   tidak ada balasan. Jangan mengirim pesan test ke chat yang tidak di-allowlist.
4. Ubah model atau base prompt di Pengaturan dan tekan **Simpan settings**.
   Overview/settings harus menunjukkan perubahan belum diterapkan. Tekan
   **Terapkan ke Agent**; runtime harus restart dan revision aktif mengikuti
   revision tersimpan. Pengaturan prompt override per-chat dan moderasi harus
   tetap ada.
5. Saat Agent berhenti, ubah dan simpan settings. Tidak boleh ada runtime yang
   start otomatis. Tekan **Jalankan Agent** dari Overview untuk memulai dengan
   revision terbaru.
6. Matikan **Start on launch**, tekan **Hentikan Agent**, lalu tutup dan buka
   kembali aplikasi. Runtime harus tetap berhenti sampai tombol Jalankan ditekan.
   Opsional, aktifkan **Start on launch**, simpan, lalu buka ulang aplikasi saat
   sesi masih paired dan settings valid; Agent boleh start tanpa membuka QR.

Jalur CLI headless dan GUI memakai composition pipeline yang sama, tetapi GUI
menggunakan settings DB dan leased data root miliknya. Data lama dari folder CLI
tidak otomatis dipindah atau diimpor; pastikan data-root/identity yang dimaksud
sudah dipilih sebelum menguji pemakaian conversation database lama.

Layar sesi tidak menyambungkan WhatsApp otomatis saat aplikasi baru dibuka.
Auto-start Agent hanya mengikuti preferensi **Start on launch** dan tidak memulai
pairing. Logout mencabut perangkat dari akun WhatsApp dan menghapus kredensial
lokal melalui library. Jangan gunakan akun utama untuk uji unlink.

## Skenario data-root dan lock

Setelah aplikasi pernah dibuka, periksa pointer dan database:

```powershell
Get-Content "$env:LOCALAPPDATA\WazzapAgent\bootstrap.json"
Get-ChildItem "$env:LOCALAPPDATA\WazzapAgent"
```

Default desktop memakai `%LOCALAPPDATA%\WazzapAgent`; pointer bootstrap dapat
menunjuk root lain pada instalasi yang sudah dipindahkan. Jalankan executable
dua kali bersamaan. Instance kedua harus gagal memperoleh `.wazzapagent.lock`;
tutup instance pertama sebelum mencoba lagi.

## Pemeriksaan otomatis

```powershell
go test -count=1 ./...
go test -tags gui -count=1 ./...
go vet ./...
go test -race -count=1 ./internal/config ./internal/control ./internal/adapters/sqlite ./internal/platform ./internal/app ./internal/account ./internal/adapters/whatsapp/hypermeow
```

## Batas saat ini

Langkah otomatis tidak membuktikan hasil pairing atau balasan provider. Uji
manual di atas memerlukan telepon, akun WhatsApp khusus, dan LLM endpoint yang
dapat diakses. Backup/restore, pemindahan data-root, dan build APK Android tetap
memerlukan pekerjaan lanjutan; Android membutuhkan native host dan
SDK/NDK/JDK/ADB yang sesuai.
