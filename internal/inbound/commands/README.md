# Command

Satu file = satu command. File itu memegang semuanya: nama, alias, permission,
parsing argumen, balasan, perubahan config, dan tombolnya sendiri. Command tidak
saling import, dan tidak ada file lain yang perlu diubah saat menambah command.

## Menambah command

Buat `internal/inbound/commands/ping.go`:

```go
package commands

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Permission:  "public",
		Description: "Replies with pong.",
		Run:         runPing,
	})
}

func runPing(ctx context.Context, c *command.Context) error {
	return c.Reply(ctx, "pong")
}
```

Selesai. Tidak ada `go generate`, tidak ada switch, tidak ada interface yang
perlu ditambah. Nama atau alias yang bentrok membuat aplikasi gagal start.

## Yang tersedia di `*command.Context`

| | |
|---|---|
| `c.Args`, `c.HasArgs` | Teks setelah `/nama `. `HasArgs` true bila ada spasi setelah nama. |
| `c.Message`, `c.Facts` | Pesan yang sudah dinormalisasi dan fakta permission (owner, admin, group, fromMe). |
| `c.Config`, `c.Agent` | Config chat saat ini dan Agent chat (history, model input). |
| `c.Reply(ctx, text)` | Kirim teks ke chat. |
| `c.ReplyButtons(ctx, text, buttons...)` | Kirim teks dengan tombol milik command ini. |
| `c.UpdateConfig(ctx, func(*agent.ConfigValues))` | Ubah config chat. Aman terhadap crash dan replay. |
| `c.ResetHistory(ctx)` | Hapus history chat. |
| `c.Group()` | Port moderasi grup (close/open/description/delete/mute/kick). |
| `c.QuotedRaw(ctx)` | Payload mentah pesan yang di-reply (khusus `/catch`). |
| `c.Commands()` | Daftar semua command (untuk `/help`). |

Framework yang mengurus sisanya: cek permission, balasan `DeniedReply`, dan
menandai pesan selesai setelah `Run` sukses. Jika `Run` mengembalikan error,
pesan tidak ditandai selesai dan recovery akan menjalankannya lagi.

## Tombol

Tombol selalu milik command yang mengirimnya. `command.Button{Label, Args}`
dikirim dengan ID `/<nama> <Args>`, jadi tap tombol masuk kembali ke `Run`
command yang sama, sama persis seperti user mengetik `/<nama> <Args>`, dengan
permission yang sama. Contoh dari `trigger.go`:

```go
return c.ReplyButtons(ctx, formatTriggers(triggers),
	command.Button{Label: "Mention: turn off", Args: "mention off"},
)
```

Tap tombol itu menjalankan `/trigger mention off`. Bila host tidak mendukung
tombol, pilihan dikirim sebagai teks berisi command yang bisa diketik.

## Permission

`Permission` adalah ekspresi boolean dengan atom `public`, `owner`/`isOwner`,
`admin`/`isAdmin`/`senderIsAdmin`, `group`/`isGroup`, `private`/`isPrivate`, dan
`fromMe`/`from_me`. `!` paling kuat, lalu `and`, lalu `or`; gunakan kurung.

```go
Permission: "(isPrivate or isAdmin or isOwner) and !fromMe",
```

Command yang dijalankan model lewat `reply_message` dianggap dari akun bot
(`fromMe=true`). Tambahkan `and !fromMe` bila bot tidak boleh menjalankannya.

## `UpdateConfig`

Fungsi perubahan harus menulis nilai absolut (`level = 2`), bukan relatif
(`level++`). Bila aplikasi crash setelah config tersimpan tetapi sebelum pesan
ditandai selesai, command dijalankan ulang; journal mengenali bahwa perubahan
sudah diterapkan karena menjalankan fungsi itu lagi tidak mengubah apa pun.
