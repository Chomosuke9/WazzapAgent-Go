# Use case pengelolaan aplikasi

Tempat operasi pengelolaan settings, sesi WhatsApp, dan lifecycle runtime yang
dipanggil UI: membaca status, validasi/simpan/terapkan settings, start/stop,
pairing, reconnect, dan logout.

- Gunakan dependency injection melalui constructor dan interface kecil sesuai kebutuhan.
- Operasi I/O dan lifecycle menerima `context.Context`.
- Serialisasikan operasi lifecycle agar satu data root tidak memiliki runtime ganda.
- Gunakan validasi dari `internal/config` dan persistence dari adapter SQLite.
- Bedakan Stop, Logout, dan Hapus Data.
- Tidak mengimpor Wails, React, atau detail HTTP.
- Tidak menduplikasi business logic Agent, inbound, policy, atau outbox.

`internal/app` tetap menjadi tempat composition runtime yang sudah ada.
Fondasi settings controller sekarang tersedia melalui `NewController` dan
`SettingsRepository`; operasi runtime/session masih ditambahkan pada paket
berikutnya. Controller tidak membuka database sendiri dan tidak mengimpor
Wails.
