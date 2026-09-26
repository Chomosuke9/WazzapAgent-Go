# Service UI netral-transport

Tempat `AppService` dan DTO yang dipakai bersama oleh GUI Wails
(`internal/adapters/wails`) dan bridge browser HTTP (`internal/adapters/web`).
Package ini hanya memanggil `internal/control` dan tidak mengimpor Wails atau
detail HTTP, sehingga setiap operasi UI cukup ditulis sekali.

- Adapter Wails menanam `*ui.AppService` sehingga binding yang dihasilkan tetap
  memakai nama tipe `wails.AppService` dan ID method yang sama. Hanya `Ping`
  yang di-override untuk mengirim event `app:ping`.
- Adapter web memanggil `*ui.AppService` lewat allowlist method di `handler.go`.
  Method baru harus ditambahkan ke allowlist agar tersedia di browser.
- DTO tidak pernah membawa nilai secret; settings hanya mengembalikan status secret.
