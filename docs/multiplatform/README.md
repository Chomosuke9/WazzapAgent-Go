# Panduan aplikasi multiplatform

DiscordAgent Go adalah port Discord dari WazzapAgent Go. Shell Wails desktop,
Android, mode browser, dan CLI headless memakai satu core Go dan satu frontend
React. Riwayat implementasi paket kerja P0–P4 (yang ditulis untuk integrasi
WhatsApp) tetap tersedia di repository asal
[Chomosuke9/WazzapAgent-Go](https://github.com/Chomosuke9/WazzapAgent-Go);
dokumen ini hanya menjelaskan keadaan kode sekarang.

- [Runbook testing manual](TESTING.md)

## Struktur

```text
DiscordAgent-Go/
├── cmd/
│   ├── discordagent/            # CLI/headless, membaca env dan .env
│   ├── app/                     # Shell Wails desktop/Android (build tag gui)
│   ├── server/                  # UI browser loopback (build tag web)
│   └── webhost/                 # UI browser untuk server, dengan access token
├── internal/
│   ├── app/                     # Composition runtime Agent
│   ├── config/                  # Settings, validasi, dan snapshot immutable
│   ├── control/                 # Use case settings, sesi bot, dan runtime
│   ├── host/                    # Host bersama untuk GUI dan web
│   ├── platform/                # Path data, lock data root, scope sesi
│   ├── ui/                      # AppService dan DTO untuk Wails dan bridge web
│   ├── adapters/
│   │   ├── discord/             # Integrasi Discord (discordgo)
│   │   ├── sqlite/              # Persistence aplikasi dan settings
│   │   ├── web/                 # Bridge HTTP untuk mode browser
│   │   └── llm/                 # Integrasi LLM OpenAI-compatible
│   └── ...                      # agent, inbound, command, policy, effect, dll.
├── frontend/                    # Satu frontend React bersama
└── build/                       # Input build/packaging per platform
```

## Batas dependency

```text
React -> binding Wails / bridge web -> internal/ui -> internal/control
                                                         |
                                               core Go / repository
                                                         |
                                            SQLite / adapter Discord
```

- Core (`agent`, `inbound`, `policy`, `effect`, `command`) tidak mengenal
  Discord. Identitas di core adalah UUID internal, `SenderRef` untuk model, dan
  `identity.UserID` (snowflake Discord) sebagai identitas peserta kanonik.
- Hanya `internal/adapters/discord` yang mengimpor `discordgo`. Tidak ada tipe
  `discordgo` yang melewati batas paket itu.
- Frontend tidak membaca database, memegang client Discord, atau menjalankan
  logika izin.

## Data persistent

```text
<platform-app-data>/
├── settings.db                  # Settings GUI typed + session binding
├── runtime-identity.json        # Identitas runtime CLI
└── tenants/
    └── <tenant-id>/
        ├── app.db               # Riwayat, outbox, konfigurasi per chat
        └── discord.token        # Token bot tertaut (mode 0600)
```

- Token bot adalah kredensial. Ia disimpan di file per scope akun, ditulis
  atomik dengan izin hanya pemilik, tidak pernah dikembalikan ke UI, dan tidak
  dicatat ke log. CLI juga dapat memakai `DISCORDAGENT_DISCORD_TOKEN` dari
  environment atau `.env` tanpa menyimpannya.
- `app.db` memakai satu migrasi skema (`001_schema.sql`). Alamat chat adalah ID
  channel Discord; chat menyimpan juga ID server (`guild_address`) dan, untuk
  thread atau DM, ID channel induk atau ID user (`alias_address`) agar
  allowlist per server/parent/user tetap berlaku setelah restart.
- Balasan lebih dari 2000 karakter dikirim sebagai beberapa pesan; ID pesan
  lanjutan disimpan di `receipt_aliases` sehingga reply ke bagian mana pun
  dihitung sebagai reply ke bot.

## Keputusan arsitektur

| ID | Keputusan |
| --- | --- |
| D01 | Satu Go module, satu React frontend. `cmd/app` shell Wails; `cmd/discordagent` CLI; `cmd/server` dan `cmd/webhost` mode browser. |
| D02 | Domain tidak mengimpor Wails atau `discordgo`. `control` mendefinisikan port; `app` dan adapter mengimplementasikannya. |
| D03 | GUI membaca `settings.db` + default Go. CLI memakai precedence process env > `.env` > default Go. |
| D05 | Satu controller dan satu runtime per data root, dijaga lock lintas proses. |
| D06 | Save menyimpan; Apply menerapkan dengan stop/rebuild/start. |
| D07 | Mode sesi saja hanya menjaga bot online (intent Guilds); tidak membuat LLM client, tidak menyimpan inbound, dan tidak mengirim respons. |
| D08 | Link memverifikasi token lewat REST (`/users/@me`) lalu gateway, dan baru menyimpan token setelah Discord menerimanya. |
| D12 | Stop mempertahankan token. Unlink menghapus file token (Discord tidak punya API untuk mencabut token bot; reset token di Developer Portal). Hapus data bukan bagian implisit dari keduanya. |
| D14 | Link ulang bot yang sama (ID bot terbaca dari bagian pertama token) memakai scope data yang sama; bot lain mendapat scope tenant+account baru. |

## Kontrak adapter Discord

- Intent gateway Agent: Guilds, Guild Messages, Direct Messages, Message Content
  (privileged), dan Guild Emojis & Stickers. Close code 4004 berarti token
  ditolak; 4013/4014 berarti intent Message Content belum diaktifkan.
- Pesan masuk dari bot (termasuk bot sendiri), webhook, dan pesan sistem
  diabaikan. Mention `<@id>` ditulis ulang menjadi token `@id` yang diikat core
  ke user; mention role bot sendiri dihitung sebagai mention bot. Lampiran dan
  stiker menjadi placeholder seperti `【image】`.
- Peran grup core dipetakan dari izin Discord di channel: superadmin = pemilik
  server atau Administrator; admin = salah satu izin moderator (Manage Server,
  Manage Channels, Manage Messages, Kick/Ban/Moderate Members). Bot dianggap
  admin bila punya Manage Messages; tiap aksi `/mod` memeriksa izinnya sendiri.
- Tombol dan menu command menjadi message components. Tap diakui dengan
  deferred update lalu diproses sebagai pesan pengguna yang me-reply pesan bot.
- `/mod mute` ditegakkan lokal: pesan baru anggota yang di-mute dihapus.
- `send_sticker` menawarkan stiker server itu sendiri; DM tidak punya stiker.

## Referensi

- [Discord Developer Portal](https://discord.com/developers/applications)
- [discordgo](https://github.com/bwmarrin/discordgo)
- [Struktur scaffold Wails](https://v3.wails.io/getting-started/your-first-app/)
- [Wails mobile](https://v3.wails.io/guides/mobile/)
