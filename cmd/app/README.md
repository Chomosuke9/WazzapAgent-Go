# Entrypoint aplikasi Wails

Tempat bootstrap shell Wails v3 untuk Windows, Linux, macOS, dan Android.
Nama `app` dipakai karena shell ini bukan khusus desktop.

`main.go` mendaftarkan `AppService` dari `internal/adapters/wails`, memuat bundle
frontend, dan membuka window. Seluruh source GUI memakai build tag `gui` supaya
pengujian core tidak memerlukan WebView atau bundle frontend.

Bootstrap memuat controller, data root, settings, sesi, dan lifecycle runtime
bersama. Android memakai `getFilesDir()` melalui bridge Wails untuk data privat
aplikasi; kode Go bisnis tetap sama dengan desktop.

Build dari root repository melalui `wails3 task build`; frontend harus diinstal
dengan npm yang sesuai lockfile. Lihat `build/README.md` untuk toolchain dan batas
dukungan platform. Shell membuka settings database dan dapat memulai sesi
WhatsApp serta Agent.
