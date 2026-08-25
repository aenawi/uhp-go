package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
)

// The reason this engine exists: a client holds a response id and comes back.
func TestSQLiteStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "uhp.db")
	ctx := context.Background()

	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	task := sampleTask("resp_a", "sess_a", storeEpoch)
	if err := first.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := first.CreateSession(ctx, sampleSession("sess_a", "claude-code", storeEpoch)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, found, err := second.GetTask(ctx, "resp_a")
	if err != nil || !found {
		t.Fatalf("get task after restart: found=%v err=%v", found, err)
	}
	assertTaskEqual(t, task, got)

	sess, found, err := second.GetSession(ctx, "sess_a")
	if err != nil || !found {
		t.Fatalf("get session after restart: found=%v err=%v", found, err)
	}
	if sess.HarnessID != "claude-code" || sess.CreatedAt != storeEpoch.Unix() {
		t.Fatalf("session did not survive intact: %+v", sess)
	}

	// The session's transcript has to survive with it, or a resumed
	// conversation comes back with no history behind it.
	tasks, err := second.ListSessionTasks(ctx, "sess_a")
	if err != nil {
		t.Fatalf("list session tasks after restart: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "resp_a" {
		t.Fatalf("transcript did not survive: %+v", tasks)
	}
}

// A file written by a newer uhpd is refused rather than written to. The
// columns this build would ignore are the ones the other binary needs, so
// opening it is data loss discovered later; refusing is a startup failure an
// operator can act on now.
func TestSQLiteStoreRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uhp.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatalf("stamp future version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("opening a newer schema must fail")
	}
}

// Reopening a database this build already understands must not try to migrate
// it again, and must not fail for having nothing to do.
func TestSQLiteStoreReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uhp.db")
	for i := 0; i < 3; i++ {
		s, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != sqliteSchemaVersion {
			t.Fatalf("schema version is %d, want %d", version, sqliteSchemaVersion)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

// A version-1 file — one written before shares existed — is brought forward
// without losing what is in it.
//
// This is the first migration this schema has had, so it is also the first
// check that the mechanism works at all on a file with rows in it. The rows
// matter: a migration that dropped a session while adding a table would pass
// every other test here, because every other test starts from an empty file.
func TestSQLiteStoreMigratesAVersionOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uhp.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seed(t, s, shareSession("sess_old"))
	// Wind the file back to what a build before shares would have left: the
	// table gone and the version stamp with it.
	if _, err := s.db.Exec(`DROP TABLE shares`); err != nil {
		t.Fatalf("drop shares: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("stamp version 1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen a version-1 file: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	ctx := context.Background()
	if _, found, err := reopened.GetSession(ctx, "sess_old"); !found || err != nil {
		t.Fatalf("the migration lost a session: found=%v err=%v", found, err)
	}
	if _, _, err := reopened.CreateShare(ctx, &domain.Share{ID: "shr_new", SessionID: "sess_old"}); err != nil {
		t.Fatalf("share a session in a migrated file: %v", err)
	}
	if _, found, err := reopened.GetShare(ctx, "shr_new"); !found || err != nil {
		t.Fatalf("get share after migration: found=%v err=%v", found, err)
	}
}

// The database holds every prompt a client sent and every answer a harness
// gave, so a fresh one is not created world-readable. SQLite gives the -wal
// and -shm files the database file's mode, so all three are checked.
func TestSQLiteStoreCreatesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "uhp.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// A write, so the WAL exists to be checked.
	if err := s.CreateTask(context.Background(), sampleTask("resp_a", "sess_a", storeEpoch)); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %s: %v", path+suffix, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("%s is mode %04o, which is readable beyond its owner", path+suffix, perm)
		}
	}
}

// An existing database is left with the permissions the operator gave it.
func TestSQLiteStoreKeepsExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "uhp.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("reopening changed the mode to %04o", perm)
	}
}

// The driver splits its DSN at the first '?', so a path carrying one would
// open a different database than the operator named.
func TestSQLiteStoreRejectsUnusablePath(t *testing.T) {
	if _, err := NewSQLiteStore(""); err == nil {
		t.Fatal("an empty path must be refused")
	}
	if _, err := NewSQLiteStore(filepath.Join(t.TempDir(), "uh?p.db")); err == nil {
		t.Fatal("a path containing '?' must be refused")
	}
}

// The engine is chosen by configuration, so what is on disk has to be readable
// by something other than this process — an operator debugging a stuck task
// should be able to open the file and look.
func TestSQLiteStoreWritesReadableRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uhp.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateTask(context.Background(), sampleTask("resp_a", "sess_a", storeEpoch)); err != nil {
		t.Fatalf("create: %v", err)
	}

	var (
		sessionID string
		createdAt int64
		kind      string
	)
	err = s.db.QueryRow(`SELECT session_id, created_at, typeof(data) FROM tasks WHERE id = ?`, "resp_a").
		Scan(&sessionID, &createdAt, &kind)
	if err != nil {
		t.Fatalf("inspect row: %v", err)
	}
	if sessionID != "sess_a" {
		t.Fatalf("session_id column is %q", sessionID)
	}
	if createdAt != storeEpoch.Unix() {
		t.Fatalf("created_at column is %d, want %d", createdAt, storeEpoch.Unix())
	}
	// TEXT, not BLOB: the document is meant to be legible to `sqlite3` and to
	// SQLite's own json functions.
	if kind != "text" {
		t.Fatalf("data column holds %s, want text", kind)
	}
}

// A schema version this build wrote is the one it reads back, and the tables
// it names are the ones that exist. Catching a typo in the DDL here beats
// catching it as a query error on the first request.
func TestSQLiteSchemaMatchesQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uhp.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, name := range []string{"tasks", "sessions"} {
		var found string
		err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
		if err == sql.ErrNoRows {
			t.Fatalf("table %s was not created", name)
		}
		if err != nil {
			t.Fatalf("inspect schema: %v", err)
		}
	}
}
