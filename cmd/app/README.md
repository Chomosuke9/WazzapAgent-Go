# Entrypoint aplikasi Wails

Tempat bootstrap shell Wails v3 untuk Windows, Linux, macOS, dan Android.
Nama `app` dipakai karena shell ini bukan khusus desktop.

`main.go` mendaftarkan `AppService` dari `internal/adapters/wails`, memuat bundle
frontend, dan membuka window. Seluruh source GUI memakai build tag `gui` supaya
pengujian core tidak memerlukan WebView atau bundle frontend.

P0 hanya menyediakan info aplikasi dan event ping. Bootstrap controller, data root,
settings, sesi, dan lifecycle runtime bersama ditambahkan pada paket berikutnya
sesuai `docs/multiplatform/README.md`. Business logic tetap berada di `internal`.
Bootstrap native Android mengikuti template resmi Wails pada versi yang dipin;
kode Go bersama tidak disalin menjadi backend Android terpisah.

Build dari root repository melalui `wails3 task build`; frontend harus diinstal
dengan npm yang sesuai lockfile. Lihat `build/README.md` untuk toolchain dan batas
dukungan platform. Shell ini tidak membuka database atau memulai WhatsApp.
