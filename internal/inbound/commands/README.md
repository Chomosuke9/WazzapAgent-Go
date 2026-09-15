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

Keluarga `/group` memiliki descriptor inbound di `group.go` dan handler yang
menerima akses native client serta target store dari adapter WhatsApp. File yang
sama juga menjadi executor bagi command yang dibawa secara internal oleh
`reply_message`; `groupcmd` hanya menyimpan grammar bersama untuk validasi pada
setiap boundary sebelum efek durable dieksekusi.
