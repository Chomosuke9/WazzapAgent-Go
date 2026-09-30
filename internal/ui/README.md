# Service UI

`AppService` dan DTO untuk GUI Wails (`cmd/app`) dan bridge browser HTTP
(`internal/adapters/web`). Package ini hanya memanggil `internal/control` dan
tidak mengimpor Wails atau detail HTTP, sehingga setiap operasi UI ditulis sekali.

- `cmd/app` mendaftarkan `*ui.AppService` langsung ke Wails; binding TypeScript
  di `frontend/bindings/.../internal/ui` dihasilkan oleh `wails3 generate bindings`.
- Adapter web memanggil method exported `*ui.AppService` berdasarkan nama. Setiap
  method exported otomatis tersedia di GUI dan browser, jadi jangan menambahkan
  method exported yang bukan operasi UI.
- Tidak ada event push ke frontend. Status sesi bot Discord hanya dicatat ke log
  (`SessionLog`), dan halaman membaca status dengan polling.
- DTO tidak pernah membawa nilai secret; settings hanya mengembalikan status secret.

## Perilaku

Service ini memanggil `internal/control`; repository, database handle, dan client
Discord tidak diekspos langsung ke frontend. Core Go tidak mengimpor package ini.

Token bot hanya dikirim sekali saat link, disimpan di file token milik akun
(hanya bisa dibaca user pemilik), dan tidak pernah dikembalikan ke UI atau
dicatat ke log.
Pembacaan settings hanya mengembalikan status secret, bukan nilai secret yang
sudah tersimpan. UI membaca status sesi dengan polling `GetDiscordSessionStatus`.

Halaman Log membaca buffer terbaru melalui `GetLogs` dan menerima catatan
terstruktur dari logger runtime serta operasi yang dipicu UI. Buffer menyimpan
maksimal 500 aktivitas di memori proses. Setiap warning dan error juga membawa
detail lengkap (semua field dan seluruh rantai error asli) yang dibuka lewat
tombol "Full error" (`GetLogDetails`), dan 200 warning/error terbaru disimpan
di `problems.jsonl` di data root agar tetap ada setelah restart. Field rahasia,
token bot, ID Discord, ID pesan, dan payload provider mentah tidak diteruskan
ke UI.

Halaman Chat membaca daftar chat dan transkrip sisi Agent melalui
`GetDiscordConversations` dan `GetDiscordMessages`. Pembacaan memakai akun
aktif dari session binding, membuka `app.db` dalam mode hanya-baca, dan
menggunakan riwayat Agent yang sudah tersimpan; UI menampilkan paling banyak
100 chat dan 100 pesan terbaru untuk satu chat. Ini bukan arsip lengkap seluruh
pesan di server Discord. Pesan masuk dan balasan Agent tidak disalin ke log aplikasi.
Saat Agent aktif dan bot Discord terhubung, halaman ini dapat mengirim pesan,
memilih pesan Agent yang terkirim untuk dihapus, serta menampilkan anggota channel
langsung dari Discord (butuh Server Members Intent). Tombol roda gigi di header percakapan membuka panel
pengaturan chat yang memuat izin moderasi per chat, instruksi khusus, dan
pengelolaan anggota channel. Pesan peserta lain dapat dihapus melalui aksi
hapus pada pesan bila bot punya izin Manage Messages; izin itu diperiksa ulang
dari Discord tepat sebelum menghapus, dan pesan peserta lain di chat pribadi tidak bisa dihapus oleh UI.
Penghapusan, kick, dan penyimpanan pengaturan meminta konfirmasi atau tindakan
eksplisit dari pengguna. Kick hanya tersedia untuk anggota non-moderator ketika
bot punya izin Kick Members, lalu izin itu diperiksa ulang sebelum kick.
Pesan yang dikirim dari UI masuk ke riwayat Agent dan dapat dipilih untuk
dihapus setelahnya. Daftar anggota memakai handle sementara; ID Discord
tidak dikirim ke UI. Tingkat moderasi dan instruksi khusus tersimpan pada
konfigurasi persisten per chat.
Nama channel ("#channel · Server") disimpan bersama chat setiap kali Agent
melihat channel itu. Sebelum nama tersedia, UI menampilkan "Channel" tanpa
menggunakan ID channel sebagai judul.

Settings tersimpan melalui `internal/control` dan repository SQLite yang dibuka
setelah data-root lease diperoleh. UI hanya menerima public view dan status secret,
sedangkan nilai secret masuk lewat patch `replace` dan tidak pernah dikembalikan.
GUI menampilkan root efektif yang sedang dilindungi lease; pemindahan root belum
menjadi operasi `SaveSettings`. Link ulang bot yang sama setelah unlink memakai
scope data yang sama; bot lain mendapat scope data baru.
Client sesi tidak memiliki handler pesan atau API kirim; seluruh aksi Chat
melewati runtime Agent yang sedang berjalan.
