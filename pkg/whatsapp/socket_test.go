package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polymorfa/hypermeow/proto/waAdv"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/store/sqlstore"
	"github.com/polymorfa/hypermeow/types"
	_ "modernc.org/sqlite"
)

func TestModerncHypermeowStoreCapability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "whatsapp.db")
	initialContainer, initialDB := openCapabilityStore(t, ctx, dbPath)
	t.Cleanup(func() { _ = initialContainer.Close() })

	assertPragma(t, ctx, initialDB, "foreign_keys", "1")
	assertPragma(t, ctx, initialDB, "journal_mode", "wal")
	assertPragma(t, ctx, initialDB, "busy_timeout", "5000")
	assertPragma(t, ctx, initialDB, "synchronous", "1")
	assertPragma(t, ctx, initialDB, "integrity_check", "ok")

	var version int
	if err := initialDB.QueryRowContext(ctx, "SELECT version FROM whatsmeow_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read Hypermeow schema version: %v", err)
	}
	if version != 18 {
		t.Fatalf("Hypermeow schema version = %d, want 18", version)
	}

	device := initialContainer.NewDevice()
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("10000000001", types.HiddenUserServer)
	device.ID = &jid
	device.LID = lid
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte("details"),
		AccountSignatureKey: bytes.Repeat([]byte{1}, 32),
		AccountSignature:    bytes.Repeat([]byte{2}, 64),
		DeviceSignature:     bytes.Repeat([]byte{3}, 64),
	}
	device.Platform = "capability-test"
	device.PushName = "WazzapAgent"
	if err := device.Save(ctx); err != nil {
		t.Fatalf("save device: %v", err)
	}

	fixture := assertRepresentativeStores(t, ctx, device)
	assertTransactionRollback(t, ctx, initialDB)

	if err := initialContainer.Close(); err != nil {
		t.Fatalf("close initial container: %v", err)
	}

	reopenedContainer, reopenedDB := openCapabilityStore(t, ctx, dbPath)
	t.Cleanup(func() { _ = reopenedContainer.Close() })
	reloaded, err := reopenedContainer.GetDevice(ctx, jid)
	if err != nil {
		t.Fatalf("reload device: %v", err)
	}
	if reloaded == nil || reloaded.PushName != "WazzapAgent" || reloaded.LID != lid {
		t.Fatalf("reloaded device mismatch: %#v", reloaded)
	}
	assertStoredValues(t, ctx, reloaded, fixture)
	assertPragma(t, ctx, reopenedDB, "foreign_key_check", "")

	backupPath := filepath.Join(t.TempDir(), "whatsapp-backup.db")
	if _, err := reopenedDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := reopenedDB.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escapeSQLiteLiteral(filepath.ToSlash(backupPath)))); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := reopenedContainer.Close(); err != nil {
		t.Fatalf("close source container: %v", err)
	}

	backupContainer, backupDB := openCapabilityStore(t, ctx, backupPath)
	t.Cleanup(func() { _ = backupContainer.Close() })
	assertPragma(t, ctx, backupDB, "integrity_check", "ok")
	backupDevice, err := backupContainer.GetDevice(ctx, jid)
	if err != nil {
		t.Fatalf("open restored device: %v", err)
	}
	if backupDevice == nil || backupDevice.PushName != "WazzapAgent" {
		t.Fatalf("restored device mismatch: %#v", backupDevice)
	}
	assertStoredValues(t, ctx, backupDevice, fixture)
	if err := backupDevice.Delete(ctx); err != nil {
		t.Fatalf("delete restored device: %v", err)
	}
	assertDeclaredForeignKeyCascades(t, ctx, backupDB, jid)
	if err := backupContainer.Close(); err != nil {
		t.Fatalf("close backup container: %v", err)
	}
}

func TestOpenDeviceStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "tenant-1", "whatsapp.db")
	container, err := OpenDeviceStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open device store: %v", err)
	}
	if err := container.Close(); err != nil {
		t.Fatalf("close device store: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat device store: %v", err)
	}

	if _, err := OpenDeviceStore(ctx, "bad\x00path", nil); err == nil {
		t.Fatal("device store accepted null byte path")
	}
	if _, err := OpenDeviceStore(ctx, "bad#path.db", nil); err == nil {
		t.Fatal("device store accepted URI-reserved path")
	}
}

func TestModerncBusyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "contention.db")
	db1 := openRawSQLite(t, ctx, path, 250)
	defer db1.Close()
	db2 := openRawSQLite(t, ctx, path, 250)
	defer db2.Close()

	if _, err := db1.ExecContext(ctx, "CREATE TABLE contention (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatalf("create contention table: %v", err)
	}
	tx, err := db1.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin writer transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO contention (value) VALUES ('first')"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("hold write lock: %v", err)
	}

	started := time.Now()
	_, err = db2.ExecContext(ctx, "INSERT INTO contention (value) VALUES ('second')")
	elapsed := time.Since(started)
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("second writer succeeded while write lock was held")
	}
	if elapsed < 100*time.Millisecond {
		_ = tx.Rollback()
		t.Fatalf("busy timeout elapsed = %s, want at least 100ms", elapsed)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
		_ = tx.Rollback()
		t.Fatalf("second writer error = %v, want busy/locked", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback writer transaction: %v", err)
	}
}

func TestModerncRecoveryAfterForcedExit(t *testing.T) {
	if os.Getenv("WAZZAP_SQLITE_CRASH_HELPER") == "1" {
		runCrashHelper()
		return
	}

	path := filepath.Join(t.TempDir(), "crash.db")
	helperCtx, helperCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer helperCancel()
	command := exec.CommandContext(helperCtx, os.Args[0], "-test.run=^TestModerncRecoveryAfterForcedExit$")
	command.Env = append(os.Environ(), "WAZZAP_SQLITE_CRASH_HELPER=1", "WAZZAP_SQLITE_CRASH_PATH="+path)
	output, err := command.CombinedOutput()
	if helperCtx.Err() != nil {
		t.Fatalf("crash helper timed out: %v", helperCtx.Err())
	}
	if err == nil {
		t.Fatalf("crash helper exited successfully, want forced exit: %q", output)
	}
	if !strings.Contains(string(output), "ready") {
		t.Fatalf("crash helper did not reach active transaction: output=%q err=%v", output, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := openRawSQLite(t, ctx, path, 5000)
	defer db.Close()
	assertPragma(t, ctx, db, "integrity_check", "ok")
	assertPragma(t, ctx, db, "foreign_key_check", "")
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM durable_rows").Scan(&count); err != nil {
		t.Fatalf("count durable rows after forced exit: %v", err)
	}
	if count != 1 {
		t.Fatalf("durable row count after forced exit = %d, want 1", count)
	}
}

func runCrashHelper() {
	path := os.Getenv("WAZZAP_SQLITE_CRASH_PATH")
	dsn := deviceStoreDSN(path, defaultBusyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		os.Exit(2)
	}
	if _, err := db.Exec("CREATE TABLE durable_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		os.Exit(3)
	}
	if _, err := db.Exec("INSERT INTO durable_rows (value) VALUES ('committed')"); err != nil {
		os.Exit(4)
	}
	tx, err := db.Begin()
	if err != nil {
		os.Exit(5)
	}
	if _, err := tx.Exec("INSERT INTO durable_rows (value) VALUES ('uncommitted')"); err != nil {
		os.Exit(6)
	}
	fmt.Println("ready")
	os.Stdout.Sync()
	os.Exit(99)
}

func openRawSQLite(t *testing.T, ctx context.Context, path string, busyTimeoutMS int) *sql.DB {
	t.Helper()
	dsn := deviceStoreDSN(path, busyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw SQLite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("ping raw SQLite: %v", err)
	}
	return db
}

func openCapabilityStore(t *testing.T, ctx context.Context, path string) (*sqlstore.Container, *sql.DB) {
	t.Helper()
	dsn := deviceStoreDSN(path, defaultBusyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("ping SQLite: %v", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite", nil)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("upgrade Hypermeow store: %v", err)
	}
	return container, db
}

type storeFixture struct {
	appStateIndexMAC []byte
	appStateKeyID    []byte
	preKeyID         uint32
}

func assertRepresentativeStores(t *testing.T, ctx context.Context, device *store.Device) storeFixture {
	t.Helper()
	fixture := storeFixture{
		appStateIndexMAC: bytes.Repeat([]byte{4}, 32),
		appStateKeyID:    []byte("app-state-key"),
	}
	identity := [32]byte{1, 2, 3}
	if err := device.Identities.PutIdentity(ctx, "15550000002:0", identity); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	if err := device.Sessions.PutSession(ctx, "15550000002:0", []byte("session")); err != nil {
		t.Fatalf("put session: %v", err)
	}
	if err := device.Sessions.PutManySessions(ctx, map[string][]byte{"15550000003:0": []byte("session-2")}); err != nil {
		t.Fatalf("put many sessions: %v", err)
	}
	preKeys, err := device.PreKeys.GetOrGenPreKeys(ctx, 2)
	if err != nil {
		t.Fatalf("generate prekeys: %v", err)
	}
	if len(preKeys) != 2 {
		t.Fatalf("generated prekeys = %d, want 2", len(preKeys))
	}
	fixture.preKeyID = preKeys[0].KeyID
	if err := device.SenderKeys.PutSenderKey(ctx, "group", "sender", []byte("sender-key")); err != nil {
		t.Fatalf("put sender key: %v", err)
	}
	if err := device.AppStateKeys.PutAppStateSyncKey(ctx, fixture.appStateKeyID, store.AppStateSyncKey{Data: []byte("app-state-key-data"), Fingerprint: []byte("fingerprint"), Timestamp: 7}); err != nil {
		t.Fatalf("put app state sync key: %v", err)
	}
	var hash [128]byte
	hash[0] = 7
	if err := device.AppState.PutAppStateVersion(ctx, "regular", 3, hash); err != nil {
		t.Fatalf("put app state version: %v", err)
	}
	if err := device.AppState.PutAppStateMutationMACs(ctx, "regular", 3, []store.AppStateMutationMAC{{IndexMAC: fixture.appStateIndexMAC, ValueMAC: bytes.Repeat([]byte{5}, 32)}}); err != nil {
		t.Fatalf("put app state mutation: %v", err)
	}
	contact := types.NewJID("15550000002", types.DefaultUserServer)
	if err := device.Contacts.PutContactName(ctx, contact, "Test", "Test User"); err != nil {
		t.Fatalf("put contact: %v", err)
	}
	chat := types.NewJID("120363000000000001", types.GroupServer)
	if err := device.ChatSettings.PutPinned(ctx, chat, true); err != nil {
		t.Fatalf("put chat settings: %v", err)
	}
	if err := device.MsgSecrets.PutMessageSecret(ctx, chat, contact, "message-1", []byte("message-secret")); err != nil {
		t.Fatalf("put message secret: %v", err)
	}
	if err := device.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: contact, Token: []byte("privacy-token"), Timestamp: time.Now().UTC(), SenderTimestamp: time.Now().UTC()}); err != nil {
		t.Fatalf("put privacy token: %v", err)
	}
	if err := device.NCTSalt.PutNCTSalt(ctx, []byte("nct-salt")); err != nil {
		t.Fatalf("put NCT salt: %v", err)
	}
	ciphertextHash := [32]byte{9}
	if err := device.EventBuffer.PutBufferedEvent(ctx, ciphertextHash, []byte("event"), time.Now().UTC()); err != nil {
		t.Fatalf("put buffered event: %v", err)
	}
	if err := device.EventBuffer.AddOutgoingEvent(ctx, chat, "message-2", "text", []byte("outgoing")); err != nil {
		t.Fatalf("put outgoing event: %v", err)
	}
	pn := types.NewJID("15550000002", types.DefaultUserServer)
	lid := types.NewJID("10000000002", types.HiddenUserServer)
	if err := device.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("put LID mapping: %v", err)
	}
	return fixture
}

func assertStoredValues(t *testing.T, ctx context.Context, device *store.Device, fixture storeFixture) {
	t.Helper()
	trusted, err := device.Identities.IsTrustedIdentity(ctx, "15550000002:0", [32]byte{1, 2, 3})
	if err != nil || !trusted {
		t.Fatalf("get identity: trusted=%v err=%v", trusted, err)
	}
	session, err := device.Sessions.GetSession(ctx, "15550000002:0")
	if err != nil || !bytes.Equal(session, []byte("session")) {
		t.Fatalf("get session: value=%q err=%v", session, err)
	}
	sessions, err := device.Sessions.GetManySessions(ctx, []string{"15550000002:0", "15550000003:0"})
	if err != nil || !bytes.Equal(sessions["15550000003:0"], []byte("session-2")) {
		t.Fatalf("get many sessions: value=%q err=%v", sessions["15550000003:0"], err)
	}
	preKey, err := device.PreKeys.GetPreKey(ctx, fixture.preKeyID)
	if err != nil || preKey == nil || preKey.KeyID != fixture.preKeyID {
		t.Fatalf("get prekey: value=%#v err=%v", preKey, err)
	}
	senderKey, err := device.SenderKeys.GetSenderKey(ctx, "group", "sender")
	if err != nil || !bytes.Equal(senderKey, []byte("sender-key")) {
		t.Fatalf("get sender key: value=%q err=%v", senderKey, err)
	}
	appStateKey, err := device.AppStateKeys.GetAppStateSyncKey(ctx, fixture.appStateKeyID)
	if err != nil || appStateKey == nil || !bytes.Equal(appStateKey.Data, []byte("app-state-key-data")) || appStateKey.Timestamp != 7 {
		t.Fatalf("get app state sync key: value=%#v err=%v", appStateKey, err)
	}
	version, hash, err := device.AppState.GetAppStateVersion(ctx, "regular")
	if err != nil || version != 3 || hash[0] != 7 {
		t.Fatalf("get app state: version=%d hash=%d err=%v", version, hash[0], err)
	}
	mutationMAC, err := device.AppState.GetAppStateMutationMAC(ctx, "regular", fixture.appStateIndexMAC)
	if err != nil || !bytes.Equal(mutationMAC, bytes.Repeat([]byte{5}, 32)) {
		t.Fatalf("get app state mutation: value=%x err=%v", mutationMAC, err)
	}
	contact := types.NewJID("15550000002", types.DefaultUserServer)
	contactInfo, err := device.Contacts.GetContact(ctx, contact)
	if err != nil || contactInfo.FullName != "Test User" {
		t.Fatalf("get contact: value=%#v err=%v", contactInfo, err)
	}
	chat := types.NewJID("120363000000000001", types.GroupServer)
	settings, err := device.ChatSettings.GetChatSettings(ctx, chat)
	if err != nil || !settings.Pinned {
		t.Fatalf("get chat settings: value=%#v err=%v", settings, err)
	}
	secret, _, err := device.MsgSecrets.GetMessageSecret(ctx, chat, contact, "message-1")
	if err != nil || !bytes.Equal(secret, []byte("message-secret")) {
		t.Fatalf("get message secret: value=%q err=%v", secret, err)
	}
	token, err := device.PrivacyTokens.GetPrivacyToken(ctx, contact)
	if err != nil || token == nil || !bytes.Equal(token.Token, []byte("privacy-token")) {
		t.Fatalf("get privacy token: value=%#v err=%v", token, err)
	}
	salt, err := device.NCTSalt.GetNCTSalt(ctx)
	if err != nil || !bytes.Equal(salt, []byte("nct-salt")) {
		t.Fatalf("get NCT salt: value=%q err=%v", salt, err)
	}
	ciphertextHash := [32]byte{9}
	buffered, err := device.EventBuffer.GetBufferedEvent(ctx, ciphertextHash)
	if err != nil || buffered == nil || !bytes.Equal(buffered.Plaintext, []byte("event")) {
		t.Fatalf("get buffered event: value=%#v err=%v", buffered, err)
	}
	format, outgoing, err := device.EventBuffer.GetOutgoingEvent(ctx, chat, types.EmptyJID, "message-2")
	if err != nil || format != "text" || !bytes.Equal(outgoing, []byte("outgoing")) {
		t.Fatalf("get outgoing event: format=%q value=%q err=%v", format, outgoing, err)
	}
	pn := types.NewJID("15550000002", types.DefaultUserServer)
	lid, err := device.LIDs.GetLIDForPN(ctx, pn)
	if err != nil || lid.User != "10000000002" {
		t.Fatalf("get LID mapping: value=%s err=%v", lid, err)
	}
}

func assertDeclaredForeignKeyCascades(t *testing.T, ctx context.Context, db *sql.DB, jid types.JID) {
	t.Helper()
	checks := []struct {
		table  string
		column string
	}{
		{table: "whatsmeow_identity_keys", column: "our_jid"},
		{table: "whatsmeow_pre_keys", column: "jid"},
		{table: "whatsmeow_sessions", column: "our_jid"},
		{table: "whatsmeow_sender_keys", column: "our_jid"},
		{table: "whatsmeow_app_state_sync_keys", column: "jid"},
		{table: "whatsmeow_app_state_version", column: "jid"},
		{table: "whatsmeow_app_state_mutation_macs", column: "jid"},
		{table: "whatsmeow_contacts", column: "our_jid"},
		{table: "whatsmeow_chat_settings", column: "our_jid"},
		{table: "whatsmeow_message_secrets", column: "our_jid"},
		{table: "whatsmeow_nct_salt", column: "our_jid"},
		{table: "whatsmeow_event_buffer", column: "our_jid"},
		{table: "whatsmeow_retry_buffer", column: "our_jid"},
	}
	for _, check := range checks {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", check.table, check.column)
		if err := db.QueryRowContext(ctx, query, jid.String()).Scan(&count); err != nil {
			t.Fatalf("count cascaded rows in %s: %v", check.table, err)
		}
		if count != 0 {
			t.Fatalf("rows in %s after device delete = %d, want 0", check.table, count)
		}
	}
}

func assertTransactionRollback(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin rollback transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)", "rollback-lid", "rollback-pn"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert rollback row: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM whatsmeow_lid_map WHERE lid = ?", "rollback-lid").Scan(&count); err != nil {
		t.Fatalf("count rollback row: %v", err)
	}
	if count != 0 {
		t.Fatalf("rollback row count = %d, want 0", count)
	}
}

func assertPragma(t *testing.T, ctx context.Context, db *sql.DB, name, want string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA "+name)
	if err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if want == "" {
			return
		}
		t.Fatalf("PRAGMA %s returned no rows", name)
	}
	var got string
	if err := rows.Scan(&got); err != nil {
		t.Fatalf("scan PRAGMA %s: %v", name, err)
	}
	if want != "" && got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", name, got, want)
	}
	if want == "" {
		t.Fatalf("PRAGMA %s reported violation %q", name, got)
	}
}

func escapeSQLiteLiteral(value string) string {
	var escaped bytes.Buffer
	for _, char := range value {
		if char == '\'' {
			escaped.WriteRune('\'')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}
