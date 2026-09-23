# Adapter Wails v3

Tempat service yang diekspos melalui generated Wails bindings, DTO untuk UI,
konversi error, event status runtime, dan implementasi pairing sink untuk UI.

Adapter memanggil `internal/control`; repository, database handle, dan client
Hypermeow tidak diekspos langsung ke frontend. Core Go tidak mengimpor adapter ini.

QR dan pairing code bersifat sementara dan tidak dicatat ke log atau database.
Pembacaan settings hanya mengembalikan status secret, bukan nilai secret yang
sudah tersimpan. Setelah subscribe event atau resume, UI membaca snapshot status.

Desktop/mobile memakai komunikasi Wails dalam proses, bukan HTTP localhost.
`AppService` menyediakan `GetAppInfo`, `Ping`, settings, dan API pengelolaan sesi
WhatsApp: status, pairing QR/kode telepon, resume, stop, reconnect, cancel, dan
logout. Status berjalan lewat event `whatsapp:session`; `Ping` tetap memakai
`app:ping`. `service.go` memakai build tag `gui`. Binding TypeScript dihasilkan
oleh Wails; jangan menulis ulang kontraknya secara manual di frontend.

Halaman Log membaca buffer terbaru melalui `GetLogs` dan menerima catatan
terstruktur dari logger runtime serta operasi yang dipicu UI. Buffer menyimpan
maksimal 500 aktivitas di memori proses dan direset saat aplikasi ditutup.
Field rahasia, QR/kode pairing, ID pesan, dan payload provider mentah tidak
diteruskan ke UI.

Halaman Chat membaca daftar chat dan transkrip sisi Agent melalui
`GetWhatsAppConversations` dan `GetWhatsAppMessages`. Pembacaan memakai akun
aktif dari session binding, membuka `app.db` dalam mode hanya-baca, dan
menggunakan riwayat Agent yang sudah tersimpan; UI menampilkan paling banyak
100 chat dan 100 pesan terbaru untuk satu chat. Ini bukan arsip lengkap seluruh
pesan akun WhatsApp. Pesan masuk dan balasan Agent tidak disalin ke log aplikasi.
Saat Agent aktif dan WhatsApp terhubung, halaman ini dapat mengirim pesan,
memilih pesan Agent yang terkirim untuk dihapus, serta menampilkan anggota grup
langsung dari WhatsApp. Tombol roda gigi di header percakapan membuka panel
pengaturan chat yang memuat izin moderasi per chat, instruksi khusus, dan
pengelolaan anggota grup. Admin grup dapat menghapus pesan peserta lain melalui
aksi hapus pada pesan; hak admin diperiksa ulang dari WhatsApp tepat sebelum
revoke, dan pesan peserta lain di chat pribadi tidak bisa dihapus oleh UI.
Penghapusan, kick, dan penyimpanan pengaturan meminta konfirmasi atau tindakan
eksplisit dari pengguna. Kick hanya tersedia untuk anggota non-admin ketika akun
bot merupakan admin grup, lalu otoritas itu diperiksa ulang sebelum kick.
Pesan yang dikirim dari UI masuk ke riwayat Agent dan dapat dipilih untuk
dihapus setelahnya. Daftar anggota memakai handle sementara; alamat WhatsApp
tidak dikirim ke UI. Tingkat moderasi dan instruksi khusus tersimpan pada
konfigurasi persisten per chat.
Nama grup disimpan bersama chat setelah Agent membaca informasi grup atau
menyegarkan daftar grup ketika akun terhubung. Sebelum nama tersedia, UI
menampilkan "Grup" tanpa menggunakan alamat `@g.us` sebagai judul.

Settings tersimpan melalui `internal/control` dan repository SQLite yang dibuka
setelah data-root lease diperoleh. UI hanya menerima public view dan status secret,
sedangkan nilai secret masuk lewat patch `replace` dan tidak pernah dikembalikan.
GUI menampilkan root efektif yang sedang dilindungi lease; pemindahan root belum
menjadi operasi `SaveSettings`. QR dan kode telepon hanya berada di memori selama
pairing, sedangkan perubahan akun setelah sesi dicabut mendapat scope data baru.
Client sesi tidak memiliki handler pesan atau API kirim; seluruh aksi Chat
melewati runtime Agent yang sedang berjalan.
