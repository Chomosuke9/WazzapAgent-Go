package sqlite

import (
	"bytes"
	"io/fs"
	"testing"
)

// A checkout with CRLF changes the migration ledger hashes even though SQLite
// executes the same SQL. Check the embedded bytes used by the shipped app so
// local and CI builds can open the same database.
func TestEmbeddedMigrationsUseLF(t *testing.T) {
	for _, migrations := range []struct {
		dir   string
		files fs.FS
	}{
		{"migrations", migrationFiles},
		{"settings_migrations", settingsMigrationFiles},
	} {
		paths, err := fs.Glob(migrations.files, migrations.dir+"/*.sql")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatalf("no embedded migrations in %s", migrations.dir)
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				contents, err := fs.ReadFile(migrations.files, path)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.ContainsRune(contents, '\r') {
					t.Fatalf("%s contains CR bytes; check out SQL with eol=lf before building to preserve migration checksums", path)
				}
			})
		}
	}
}
