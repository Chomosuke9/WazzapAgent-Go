# Runbook testing manual

Runbook ini menguji shell Wails, persistence settings, pengelolaan bot Discord,
runtime Agent yang memakai core headless existing, masking secret, dan ownership
data-root. Gunakan aplikasi Discord dan server khusus untuk pengujian; jangan
masukkan API key produksi.

## Build lokal Windows

Dari PowerShell pada root repository:

```powershell
$ErrorActionPreference = "Stop"
$repo = "C:\Users\bagus\Project\DiscordAgent-Go"
$node = "C:\Users\bagus\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe"
$pnpm = "C:\Users\bagus\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\node_modules\pnpm\bin\pnpm.cjs"

Push-Location "$repo\frontend"
& $node $pnpm dlx npm@11.6.2 ci
& $node $pnpm dlx npm@11.6.2 run typecheck
& $node $pnpm dlx npm@11.6.2 run build
Pop-Location

wails3 generate bindings -f "-tags gui" -clean=true -ts -i ./cmd/app
go test -tags gui ./...
go build -tags gui,production -trimpath -ldflags="-s -w -H windowsgui" -o bin/DiscordAgent.exe ./cmd/app
```

Jika Node dan npm sudah tersedia di `PATH`, tiga perintah frontend dapat
diganti dengan `npm ci`, `npm run typecheck`, dan
`npm run build`. `wails3 task package` juga dapat digunakan pada host yang
memenuhi precondition npm.

Jalankan executable:

```powershell
.\bin\DiscordAgent-P4-test.exe
```

## Skenario persistence

1. Buka halaman **Pengaturan**.
2. Ubah nama assistant, owner user ID sintetis, allowlist, prompt, endpoint/model,
   satu angka batas, dan satu duration seperti `30s`.
3. Masukkan secret test pada salah satu field API key, lalu tekan **Simpan
   settings**.
4. Pastikan badge berubah menjadi **Tersimpan** dan revision bertambah.
5. Tutup aplikasi dengan normal, jalankan executable lagi, lalu pastikan semua
   nilai non-secret kembali sama.
6. Pastikan input secret kosong dan hanya label **tersimpan** yang terlihat.

`Save` hanya menyimpan settings. Ia tidak memulai atau menghentikan Agent.
Link bot diuji terpisah di bawah.

## Skenario bot Discord

1. Buat aplikasi di Discord Developer Portal, buka **Bot**, aktifkan **Message
   Content Intent** (dan **Server Members Intent** untuk daftar anggota), lalu
   reset dan salin token.
2. Jalankan executable dan buka halaman **Discord**. Link harus bisa dimulai
   walaupun owner, allowlist, prompt, dan API key LLM belum diisi.
3. Tempel token dan tekan **Link bot**. Status harus menjadi **Online** dan
   menampilkan nama serta ID bot. Token yang salah harus gagal dengan pesan
   yang jelas dan tidak tersimpan.
4. Buka tautan undangan, tambahkan bot ke server test, lalu kirim pesan. Mode
   sesi saja tidak boleh membalas atau menjalankan Agent.
5. Tutup aplikasi dengan normal, buka lagi, lalu pilih **Connect bot**. Token
   tidak diminta lagi.
6. Tekan **Go offline**, lalu sambungkan lagi. Stop harus mempertahankan token.
7. Tekan **Unlink bot** dan konfirmasi. File `discord.token` harus hilang dan
   status meminta link ulang. Link lagi dengan bot yang sama: riwayat chat lama
   harus tetap terlihat. Link dengan bot lain: riwayat mulai kosong.

## Skenario Agent UI dan balasan nyata

1. Pastikan bot test sudah tertaut. Pada **Pengaturan**, isi nama assistant,
   owner (user ID Discord Anda), allowlist (ID channel atau server test),
   prompt, LLM endpoint/model, dan API key yang khusus untuk pengujian. Aktifkan
   mode Discord dan Agent, lalu tekan **Simpan settings**.
2. Pada **Overview**, tekan **Jalankan Agent**. Tunggu status Agent berjalan dan
   Discord berstatus online. Halaman Discord harus menyatakan bot sedang dipakai
   Agent; operasi link/unlink tidak boleh tersedia.
3. Di channel yang masuk allowlist, mention bot dengan pertanyaan sederhana.
   Pastikan balasan datang dari bot sebagai reply, lalu periksa log lokal bila
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
   bot masih tertaut dan settings valid; Agent boleh start otomatis.

Jalur CLI headless dan GUI memakai composition pipeline yang sama, tetapi GUI
menggunakan settings DB dan leased data root miliknya. Data lama dari folder CLI
tidak otomatis dipindah atau diimpor; pastikan data-root/identity yang dimaksud
sudah dipilih sebelum menguji pemakaian conversation database lama.

Layar sesi tidak menyambungkan bot otomatis saat aplikasi baru dibuka.
Auto-start Agent hanya mengikuti preferensi **Start on launch**. Unlink hanya
menghapus token lokal; untuk membuat token lama tidak berlaku di mana pun,
reset token di Developer Portal.

## Skenario data-root dan lock

Setelah aplikasi pernah dibuka, periksa pointer dan database:

```powershell
Get-Content "$env:LOCALAPPDATA\DiscordAgent\bootstrap.json"
Get-ChildItem "$env:LOCALAPPDATA\DiscordAgent"
```

Default desktop memakai `%LOCALAPPDATA%\DiscordAgent`; pointer bootstrap dapat
menunjuk root lain pada instalasi yang sudah dipindahkan. Jalankan executable
dua kali bersamaan. Instance kedua harus gagal memperoleh `.discordagent.lock`;
tutup instance pertama sebelum mencoba lagi.

## Pemeriksaan otomatis

```powershell
go test -count=1 ./...
go test -tags gui -count=1 ./...
go vet ./...
go test -race -count=1 ./internal/config ./internal/control ./internal/adapters/sqlite ./internal/platform ./internal/app ./internal/account ./internal/adapters/discord
```

## Batas saat ini

Langkah otomatis tidak membuktikan koneksi gateway atau balasan provider. Uji
manual di atas memerlukan aplikasi bot Discord khusus, server test, dan LLM
endpoint yang dapat diakses. Backup/restore, pemindahan data-root, dan build APK Android tetap
memerlukan pekerjaan lanjutan; Android membutuhkan native host dan
SDK/NDK/JDK/ADB yang sesuai.
