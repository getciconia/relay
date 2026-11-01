// Package store keeps the relay's data in one SQLite file: trackers, their
// positions, areas, Home Assistant connections, link requests and settings.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go, no cgo: one static binary
)

// ErrNotFound is returned when a row doesn't exist.
var ErrNotFound = errors.New("not found")

// Store is the relay's database.
type Store struct {
	db      *sql.DB
	now     func() time.Time
	changes chan struct{}
}

// Open opens (or creates) the database at path and brings its schema up to
// date.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite writes one at a time; one connection avoids "database is locked"
	s := &Store{db: db, now: time.Now, changes: make(chan struct{}, 1)}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Now is the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// Changes signals after a tracker was added, renamed or removed, or sent new
// positions. Several changes in a row may come as one signal.
func (s *Store) Changes() <-chan struct{} { return s.changes }

// changed signals Changes when err is nil, and returns err.
func (s *Store) changed(err error) error {
	if err == nil {
		select {
		case s.changes <- struct{}{}:
		default:
		}
	}
	return err
}

var migrations = []string{
	`CREATE TABLE trackers (
		id INTEGER PRIMARY KEY,
		imei TEXT NOT NULL UNIQUE,
		public_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT 'Teltonika ATM700',
		status TEXT NOT NULL DEFAULT 'waiting', -- waiting: knocked, not added yet; active
		loxone_number INTEGER,
		first_seen_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		added_at INTEGER
	)`,
	`CREATE TABLE positions (
		id INTEGER PRIMARY KEY,
		tracker_id INTEGER NOT NULL REFERENCES trackers(id) ON DELETE CASCADE,
		fix_time INTEGER NOT NULL,     -- unix ms, measured by the tracker
		received_at INTEGER NOT NULL,  -- unix ms
		latitude REAL NOT NULL,
		longitude REAL NOT NULL,
		altitude INTEGER NOT NULL,
		angle INTEGER NOT NULL,
		speed INTEGER NOT NULL,
		satellites INTEGER NOT NULL,
		priority INTEGER NOT NULL,
		event_io INTEGER NOT NULL,
		io TEXT NOT NULL               -- JSON {"io113": {"s": 1, "v": 87}}
	)`,
	`CREATE INDEX positions_latest ON positions (tracker_id, fix_time)`,
	`CREATE INDEX positions_received ON positions (received_at)`,
	`CREATE TABLE areas (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		latitude REAL NOT NULL,
		longitude REAL NOT NULL,
		radius INTEGER NOT NULL,
		color TEXT NOT NULL,
		loxone_number INTEGER,
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE connections (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		token_digest TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER
	)`,
	`CREATE TABLE link_requests (
		id INTEGER PRIMARY KEY,
		user_code TEXT NOT NULL UNIQUE,
		device_code_digest TEXT NOT NULL UNIQUE,
		client_name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending', -- pending, approved, denied, consumed
		expires_at INTEGER NOT NULL,
		approved_at INTEGER
	)`,
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Setting reads a setting, creating it with make() the first time.
func (s *Store) Setting(ctx context.Context, key string, make func() string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == nil || !errors.Is(err, sql.ErrNoRows) || make == nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return v, err
	}
	v = make()
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, v)
	if err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, err
}

// SetSetting writes a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// RandomToken is prefix + 32 random bytes as hex.
func RandomToken(prefix string, size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// Digest is how tokens and device codes are kept: SHA-256, hex.
func Digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}
