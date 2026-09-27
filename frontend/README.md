# Frontend bersama

Tempat satu aplikasi React + TypeScript + Vite untuk desktop, Android, dan
nantinya browser. Tidak ada frontend terpisah per platform.

| Folder | Tanggung jawab |
| --- | --- |
| `src/components` | Komponen UI reusable, termasuk form dan indikator status |
| `src/pages` | Overview, WhatsApp, Settings, serta App & Data |
| `src/layouts` | Layout responsive sidebar desktop dan navigasi mobile |
| `src/hooks` | React hooks untuk state lokal, context, dan subscription |
| `src/services` | Pemanggilan binding Go dan pemetaan response untuk UI |
| `src/styles` | Style bersama, responsive layout, safe area, dan theme |
| `src/assets` | Asset yang diproses bundler |
| `public` | Asset statis publik, tanpa secret atau data runtime |
| `bindings` | Hasil generator Wails, bukan tempat kode service manual |

Mulai dengan React state/context. Tambahkan store lain hanya ketika diperlukan.
Business logic, validasi otoritatif, sesi WhatsApp, serta pengaturan persistent
dimiliki backend Go. Jangan menyimpan kredensial atau data penting di localStorage,
source frontend, atau environment Vite.

React `19.1.1` + TypeScript `5.9.2` + Vite `8.2.0` memakai runtime Wails
`v3.0.0-beta.23` dan binding Go yang dihasilkan generator resmi.

Perintah frontend:

```text
npm ci
npm run dev
npm run typecheck
npm run build        # bundle desktop/Android (dist)
npm run build:web    # bundle browser (web-dist)
```

Semua panggilan backend lewat `src/services/backend.ts`: satu fungsi `call`
yang memakai binding Wails di desktop dan `POST /api/call` di browser, dengan
nama method dan argumen yang sama. Tidak ada event push; halaman melakukan
polling untuk status yang berubah.
