package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"

	// The driver is pure Go. That is the requirement, not a preference: the
	// image builds with CGO_ENABLED=0, so a cgo-linked SQLite would not be in
	// the binary this repository actually ships.
	_ "modernc.org/sqlite"
)

// SQLiteStore keeps tasks and sessions in a single SQLite file, satisfying
// service.Store.
//
// It exists because everything above it is lost on restart: a client stores a
// response id, comes back, and gets a 404 for work this server actually did.
// The specification is silent on storage — no engine, no durability guarantee,
// no retention rules — so this is a product decision rather than a conformance
// one, and the decision is the same one the reference implementation makes:
// the state lives on a volume the operator owns.
//
// Being the second implementation of service.Store is the other half of the
// point. An interface with one implementation is a description of that
// implementation; two is what turns MemoryStore's copy semantics, ordering and
// paging into a contract, which is why the suite in store_contract_test.go
// runs against both rather than against either.
type SQLiteStore struct {
	db *sql.DB
}

// sqliteSchemaVersion is written to `PRAGMA user_version` and checked on open.
// It is the file format's version, so it changes when a migration is added and
// not when this package is edited.
const sqliteSchemaVersion = 1

// The columns are what has to be searched, ordered or filtered; everything
// else lives in `data` as one JSON document. Splitting a task across nineteen
// columns would buy nothing — no query selects on a task's usage or its error
// code — and would turn every field added to domain.Task into a migration.
//
// `created_at` is Unix nanoseconds and is derived: the authoritative timestamp
// is the one inside `data`, and this copy exists only so the index can order
// on it. See sortKey.
var sqliteSchema = []string{
	`CREATE TABLE IF NOT EXISTS tasks (
		id         TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		data       TEXT NOT NULL
	)`,
	// ListSessionTasks reads a session's tasks oldest first, and this index is
	// the whole of that query: the same order, and the id already there to
	// break ties.
	`CREATE INDEX IF NOT EXISTS tasks_by_session ON tasks (session_id, created_at, id)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		harness_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		data       TEXT NOT NULL
	)`,
	// Newest first with the tie-break ascending, which is the exact order
	// ListSessions pages over. An index in any other direction would leave the
	// paging query sorting the whole table on every page.
	`CREATE INDEX IF NOT EXISTS sessions_by_recency ON sessions (created_at DESC, id ASC)`,
	`CREATE INDEX IF NOT EXISTS sessions_by_harness ON sessions (harness_id, created_at DESC, id ASC)`,
}

// NewSQLiteStore opens (or creates) the database at path and brings its schema
// up to date.
//
// A missing file is a new database, not an error: the first start of a server
// has no tasks and that is a valid state.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("store: sqlite path is empty")
	}
	// The driver splits its DSN at the first '?', so a path containing one
	// would open a different database than the operator named — silently, and
	// with the difference visible only as missing data. There is a URI form
	// that can escape it, and it brings its own rules on every platform;
	// refusing the character is the answer that cannot open the wrong file.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("store: sqlite path must not contain '?': %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create sqlite directory: %w", err)
	}
	if err := createPrivate(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path+"?"+strings.Join([]string{
		// A second process on the same volume waits rather than failing.
		"_pragma=busy_timeout(5000)",
		// WAL so a reader never blocks the writer. Every request that
		// streams reads this database while the run writing to it is still
		// going.
		"_pragma=journal_mode(WAL)",
		// NORMAL, not FULL, and the reason is the write rate: the service
		// calls UpdateTask on every streamed delta, so FULL would put an
		// fsync between a harness and each fragment of its answer. NORMAL
		// still survives a crash of this process; what it gives up is the
		// last few commits if the machine loses power, which is a trade worth
		// naming rather than a default left unread.
		"_pragma=synchronous(NORMAL)",
		// Every transaction takes its write lock up front. A deferred one
		// that starts by reading and then upgrades is how two writers deadlock
		// instead of queueing, and AppendArtifact is exactly that shape.
		"_txlock=immediate",
	}, "&"))
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}

	// One connection. SQLite serialises writers whatever the pool does, and
	// every read here is a single-row lookup or one page of a listing, so
	// further connections would buy contention handling for contention that
	// cannot arise — while multiplying the driver's per-connection state.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// sql.Open does not connect, so without this an unwritable path is not
	// discovered until the first request — long after the operator who could
	// have fixed it stopped watching the logs.
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open sqlite %s: %w", path, err)
	}
	if err := migrateSQLite(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// Close releases the database. Every write is committed before its call
// returns, so nothing is lost by skipping it; it is here so tests can let go
// of a temporary file and so a shutdown checkpoints the WAL rather than
// leaving one behind.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// createPrivate makes sure a database this call is about to create exists with
// 0600 before SQLite opens it.
//
// SQLite would create it at 0666 less the umask, which on a shared host leaves
// every client's prompts and every harness's answers readable by anyone with
// an account. FileHarnesses takes the same care with its document, and for a
// weaker reason: that one may carry a bearer token, this one carries the
// conversations.
//
// Creating it here rather than chmod-ing afterwards is deliberate: SQLite
// gives the -wal and -shm files the mode of the database file, so the mode has
// to be right before the first connection rather than after it. A file that
// already exists is left as the operator has it.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: create sqlite database: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: create sqlite database: %w", err)
	}
	return nil
}

func migrateSQLite(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("store: read sqlite schema version: %w", err)
	}
	if version == sqliteSchemaVersion {
		return nil
	}
	// A file written by a newer uhpd is not one this build can safely write
	// to: the columns it would ignore are the ones the other binary needs.
	// Refusing is a startup failure an operator can act on; opening it is data
	// loss they find out about later.
	if version > sqliteSchemaVersion {
		return fmt.Errorf("store: sqlite database is schema version %d, this build understands %d", version, sqliteSchemaVersion)
	}

	// DDL and the version stamp go in together. Half a schema recorded as a
	// whole one is the failure mode a migration has to rule out, and SQLite's
	// DDL is transactional, so ruling it out costs nothing.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin sqlite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range sqliteSchema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: apply sqlite schema: %w", err)
		}
	}
	// A pragma cannot take a bound parameter, so the value is formatted in.
	// It is a constant in this package and never reaches here from input.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sqliteSchemaVersion)); err != nil {
		return fmt.Errorf("store: stamp sqlite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit sqlite migration: %w", err)
	}
	return nil
}

func (s *SQLiteStore) CreateTask(ctx context.Context, t *domain.Task) error {
	data, err := encodeTask(t)
	if err != nil {
		return err
	}
	// Upsert, because MemoryStore's CreateTask overwrites and the two engines
	// answer to one contract. Creating the same id twice is not something the
	// service does; an engine that rejected it would differ from the other one
	// only in how a bug elsewhere surfaced.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, session_id, created_at, data) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			created_at = excluded.created_at,
			data       = excluded.data`,
		t.ID, t.SessionID, sortKey(t.CreatedAt), data)
	if err != nil {
		return fmt.Errorf("store: create task %s: %w", t.ID, err)
	}
	return nil
}

func (s *SQLiteStore) UpdateTask(ctx context.Context, t *domain.Task) error {
	data, err := encodeTask(t)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET session_id = ?, created_at = ?, data = ? WHERE id = ?`,
		t.SessionID, sortKey(t.CreatedAt), data, t.ID)
	if err != nil {
		return fmt.Errorf("store: update task %s: %w", t.ID, err)
	}
	// SQLite counts every row the statement matched, not only those whose
	// values changed, so an update that rewrites a task identically still
	// reports one row — which is what lets "no rows" mean "no such task".
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update task %s: %w", t.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: task %s not found", t.ID)
	}
	return nil
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM tasks WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: task %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get task %s: %w", id, err)
	}
	return decodeTask(id, data)
}

// AppendArtifact adds one artifact to a task's list.
//
// Read, decode, append, write — inside one transaction, because two harness
// updates producing files at the same time would otherwise each append to the
// list they read and one of the two files would not be there afterwards. The
// connection's _txlock=immediate takes the write lock at BEGIN, so the second
// caller queues rather than reading a document it is about to lose.
func (s *SQLiteStore) AppendArtifact(ctx context.Context, taskID string, a domain.Artifact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: append artifact to %s: %w", taskID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var data string
	err = tx.QueryRowContext(ctx, `SELECT data FROM tasks WHERE id = ?`, taskID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: task %s not found", taskID)
	}
	if err != nil {
		return fmt.Errorf("store: append artifact to %s: %w", taskID, err)
	}
	task, err := decodeTask(taskID, data)
	if err != nil {
		return err
	}
	task.Artifacts = append(task.Artifacts, a)
	updated, err := encodeTask(task)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET data = ? WHERE id = ?`, updated, taskID); err != nil {
		return fmt.Errorf("store: append artifact to %s: %w", taskID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: append artifact to %s: %w", taskID, err)
	}
	return nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, sess *domain.Session) error {
	data, err := encodeSession(sess)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, harness_id, created_at, data) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			harness_id = excluded.harness_id,
			created_at = excluded.created_at,
			data       = excluded.data`,
		sess.ID, sess.HarnessID, sortKey(sess.CreatedAt), data)
	if err != nil {
		return fmt.Errorf("store: create session %s: %w", sess.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: session %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session %s: %w", id, err)
	}
	return decodeSession(id, data)
}

func (s *SQLiteStore) UpdateSession(ctx context.Context, sess *domain.Session) error {
	data, err := encodeSession(sess)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET harness_id = ?, created_at = ?, data = ? WHERE id = ?`,
		sess.HarnessID, sortKey(sess.CreatedAt), data, sess.ID)
	if err != nil {
		return fmt.Errorf("store: update session %s: %w", sess.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update session %s: %w", sess.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: session %s not found", sess.ID)
	}
	return nil
}

// ListSessions returns one page of sessions, newest first.
//
// The order is total — newest created_at first, ties broken by id ascending —
// for the reason MemoryStore gives: cursor paging over an unstable order
// silently skips and repeats rows.
func (s *SQLiteStore) ListSessions(ctx context.Context, f domain.SessionFilter) (domain.SessionPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var (
		where []string
		args  []any
	)
	if f.HarnessID != "" {
		where = append(where, `harness_id = ?`)
		args = append(args, f.HarnessID)
	}
	if f.Cursor != "" {
		after, found, err := s.cursorPosition(ctx, f)
		if err != nil {
			return domain.SessionPage{}, err
		}
		// A cursor is the id of the previous page's last row, so the boundary
		// is that row's place in the order — an id alone orders nothing. When
		// the row is not there, or this filter cannot see it, there is no
		// boundary to resume from and the listing starts at the beginning
		// rather than guessing; that is also what MemoryStore does, and
		// guessing would skip rows the client has never seen.
		if found {
			where = append(where, `(created_at < ? OR (created_at = ? AND id > ?))`)
			args = append(args, after, after, f.Cursor)
		}
	}

	query := `SELECT data FROM sessions`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC, id ASC LIMIT ?`
	// One row past the page. UHP forbids making a client infer the end from a
	// short page — the heuristic is wrong exactly when a page is full — so the
	// question is answered rather than guessed at.
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SessionPage{}, fmt.Errorf("store: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	page := make([]*domain.Session, 0, limit)
	more := false
	for rows.Next() {
		if len(page) == limit {
			more = true
			break
		}
		var data string
		if err := rows.Scan(&data); err != nil {
			return domain.SessionPage{}, fmt.Errorf("store: list sessions: %w", err)
		}
		sess, err := decodeSession("", data)
		if err != nil {
			return domain.SessionPage{}, err
		}
		page = append(page, sess)
	}
	if err := rows.Err(); err != nil {
		return domain.SessionPage{}, fmt.Errorf("store: list sessions: %w", err)
	}

	next := ""
	if more && len(page) > 0 {
		next = page[len(page)-1].ID
	}
	return domain.SessionPage{Sessions: page, NextCursor: next}, nil
}

// cursorPosition resolves a cursor id to the ordering value of its row, under
// the same filter the listing uses.
func (s *SQLiteStore) cursorPosition(ctx context.Context, f domain.SessionFilter) (int64, bool, error) {
	query := `SELECT created_at FROM sessions WHERE id = ?`
	args := []any{f.Cursor}
	if f.HarnessID != "" {
		query += ` AND harness_id = ?`
		args = append(args, f.HarnessID)
	}
	var createdAt int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: resolve session cursor: %w", err)
	}
	return createdAt, true, nil
}

// ListSessionTasks returns a session's tasks in the order they ran.
func (s *SQLiteStore) ListSessionTasks(ctx context.Context, sessionID string) ([]*domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT data FROM tasks WHERE session_id = ? ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks for %s: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*domain.Task, 0, 4)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("store: list tasks for %s: %w", sessionID, err)
		}
		task, err := decodeTask("", data)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tasks for %s: %w", sessionID, err)
	}
	return out, nil
}

// sortKey is the ordering value written to a created_at column. The
// authoritative timestamp is the one inside the JSON document; this is derived
// and only has to order.
//
// UnixNano is undefined outside 1678–2262 and wraps rather than saturating, so
// the zero time — which is the earliest instant there is — would come back as
// a large positive number and sort as the newest row in the table. Saturating
// puts it where MemoryStore's comparison would.
//
// Two distinct out-of-range instants do collapse to the same key, so SQLite
// breaks their tie by id where MemoryStore would order them by time. Nothing
// this server mints lands there — CreatedAt comes from time.Now — and the
// alternative is a wrap that misorders the one out-of-range value that is
// reachable, the zero time of a task built without one.
func sortKey(t time.Time) int64 {
	switch {
	case t.Year() < 1678:
		return math.MinInt64
	case t.Year() > 2262:
		return math.MaxInt64
	default:
		return t.UnixNano()
	}
}
