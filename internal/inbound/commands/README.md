# Inbound command modules

Buat satu file Go untuk setiap slash command di folder ini. File tersebut
harus mengekspor satu `command.Descriptor` yang berisi `Name`, `Aliases`,
`Capability`, `Permission`, metadata bantuan, dan `Handler`.

`Permission` adalah ekspresi boolean yang dievaluasi untuk setiap invocation.
Atom yang tersedia adalah `public`, `owner`/`isOwner`, `admin`/`isAdmin`,
`group`/`isGroup`, `private`/`isPrivate`, dan `fromMe`/`from_me`. Operator
`!` memiliki prioritas tertinggi, lalu `and`, lalu `or`; gunakan tanda kurung
untuk memperjelas. Contoh:

```go
Permission: "isPrivate or isAdmin or isOwner",
```

Untuk mencegah bot menjalankan command tersebut, tambahkan `and !fromMe`:

```go
Permission: "(isPrivate or isAdmin or isOwner) and !fromMe",
```

Semua descriptor, termasuk command berbahaya, tetap di-inject ke registry.
`Permission` hanya menentukan siapa yang boleh menjalankan command pada
invocation itu; `Capability` tetap menjadi identitas fitur/efek yang dipakai
oleh policy dan boundary side effect.

Registrasi command tidak menggunakan `switch`/`case`. Generator mencari setiap
variabel exported bertipe `command.Descriptor`, lalu memasukkannya ke slice
`Descriptors` dalam `registry_gen.go`. Saat aplikasi dimulai, registry membuat
pemetaan dari `Name` dan setiap `Aliases` ke descriptor tersebut. Pesan seperti
`/example` kemudian di-parse melalui pemetaan itu dan `Handler` milik descriptor
yang cocok langsung dijalankan. Nama atau alias yang duplikat akan membuat
inisialisasi registry gagal.

Contoh alurnya:

```text
1. Tambah `example.go` dengan `var ExampleCommand = command.Descriptor{...}`.
2. Isi `Permission` secara eksplisit dan jalankan `go generate ./...` dari repository root.
3. Jalankan `go test ./...` lalu build binary.
```

Jangan mengedit `registry_gen.go` secara manual. Generator mengurutkan descriptor
secara deterministik dan CI akan gagal bila file generated belum diperbarui.

Grammar, validasi argumen, mutasi, dan penulisan respons sebuah command harus
tinggal bersama handler pada file command tersebut. Package `internal/command`
hanya menyediakan registry dan payload jurnal storage; package itu bukan tempat
parser atau implementasi command.

Setiap handler menerima `command.Adapter`, bukan `any`:

```go
func handleExample(
	ctx context.Context,
	input command.Context,
	adapter command.Adapter,
) error
```

`command.Adapter` menyediakan `SendText`. Handler bertanggung jawab penuh atas
siklus responsnya sendiri: membuat `identity.ActionID`, menyusun
`action.SendTextRequest` dari tenant/account/chat pesan inbound, memanggil
`adapter.SendText`, lalu memanggil `input.Store.MarkCommandHandled` hanya setelah
pengiriman berhasil. Tidak ada helper pengiriman bersama; respons untuk format
argumen yang salah juga harus mengikuti alur yang sama.

```go
actionID, err := identity.NewActionID()
if err != nil {
	return agent.NewError(agent.ErrorInternal, "create example response ID", err)
}
key := agent.Key{
	TenantID:  input.Message.TenantID,
	AccountID: input.Message.AccountID,
	ChatID:    input.Message.ChatID,
}
if _, err := adapter.SendText(ctx, action.SendTextRequest{
	Key: key, ActionID: actionID, Text: response,
}); err != nil {
	return err
}
return input.Store.MarkCommandHandled(ctx, input.Message)
```

Jangan menandai command selesai sebelum `SendText` berhasil. Mengirim tanpa
`MarkCommandHandled` akan meninggalkan command dalam keadaan belum selesai dan
dapat membuat recovery memprosesnya kembali.

Keluarga `/group` memiliki descriptor inbound di `group.go` dan handler yang
menerima `command.Adapter` untuk respons teks. Handler tersebut memperluas
adapter menjadi `WhatsAppCommandAdapter` untuk memperoleh akses native client
dan target store. File yang sama juga menjadi executor bagi command yang dibawa
secara internal oleh `reply_message`; `groupcmd` hanya menyimpan grammar bersama
untuk validasi pada setiap boundary sebelum efek durable dieksekusi.
