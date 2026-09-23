# Integrasi sistem operasi

Tempat resolusi lokasi data dan lifecycle khusus OS yang dibutuhkan aplikasi.
Implementasi Windows, Linux, macOS, dan Android ditambahkan ketika fiturnya
dibangun, menggunakan file Go dengan suffix/build constraint yang sesuai.

Target lokasi data:

| Platform | Lokasi default yang direncanakan |
| --- | --- |
| Windows | `%LOCALAPPDATA%/WazzapAgent/` |
| Linux | `$XDG_DATA_HOME/wazzapagent/` atau `~/.local/share/wazzapagent/` |
| macOS | `~/Library/Application Support/WazzapAgent/` |
| Android | Internal app storage dari Android context |

Jangan memakai working directory sebagai lokasi default data aplikasi GUI.
Android mengikuti lifecycle native; data persistent tidak berarti runtime selalu
berjalan di background. Integrasi background service merupakan pekerjaan terpisah.

Build constraint Linux desktop harus mengecualikan Android (`linux && !android`).
Implementasi macOS khusus desktop memakai `darwin && !ios`; iOS belum ditargetkan.
Wails-specific hooks tetap di adapter Wails, bukan disebarkan ke domain Go.

Status: resolver path desktop, bootstrap pointer, dan lease data-root sudah
tersedia secara lokal. Integrasi Android/native lifecycle dan operasi pindah
root masih belum tersedia.
