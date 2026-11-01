package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Tracker statuses.
const (
	Waiting = "waiting" // connected, but not added in the panel: refused, nothing stored
	Active  = "active"
)

// Tracker is one tracker the relay knows about.
type Tracker struct {
	ID           int64
	IMEI         string
	PublicID     string // device_id in the API, "dv_…"
	Name         string
	Model        string
	Status       string
	LoxoneNumber int // 0 = not numbered yet
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	AddedAt      *time.Time
}

const trackerColumns = `id, imei, public_id, name, model, status, COALESCE(loxone_number, 0), first_seen_at, last_seen_at, added_at`

func scanTracker(row interface{ Scan(...any) error }) (Tracker, error) {
	var t Tracker
	var first, last int64
	var added sql.NullInt64
	err := row.Scan(&t.ID, &t.IMEI, &t.PublicID, &t.Name, &t.Model, &t.Status, &t.LoxoneNumber, &first, &last, &added)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.FirstSeenAt, t.LastSeenAt, t.AddedAt = fromMS(first), fromMS(last), nullTime(added)
	return t, err
}

func (s *Store) trackers(ctx context.Context, where string, args ...any) ([]Tracker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+trackerColumns+` FROM trackers WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tracker
	for rows.Next() {
		t, err := scanTracker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveTrackers lists added trackers, oldest first.
func (s *Store) ActiveTrackers(ctx context.Context) ([]Tracker, error) {
	return s.trackers(ctx, `status = 'active' ORDER BY added_at, id`)
}

// WaitingTrackers lists trackers that connected but aren't added, newest first.
func (s *Store) WaitingTrackers(ctx context.Context) ([]Tracker, error) {
	return s.trackers(ctx, `status = 'waiting' ORDER BY last_seen_at DESC`)
}

// TrackerByID finds a tracker.
func (s *Store) TrackerByID(ctx context.Context, id int64) (Tracker, error) {
	return scanTracker(s.db.QueryRowContext(ctx, `SELECT `+trackerColumns+` FROM trackers WHERE id = ?`, id))
}

// ActiveTrackerByPublicID finds an added tracker by its API id.
func (s *Store) ActiveTrackerByPublicID(ctx context.Context, publicID string) (Tracker, error) {
	return scanTracker(s.db.QueryRowContext(ctx, `SELECT `+trackerColumns+` FROM trackers WHERE public_id = ? AND status = 'active'`, publicID))
}

// TrackerByIMEI finds a tracker, waiting or added.
func (s *Store) TrackerByIMEI(ctx context.Context, imei string) (Tracker, error) {
	return scanTracker(s.db.QueryRowContext(ctx, `SELECT `+trackerColumns+` FROM trackers WHERE imei = ?`, imei))
}

// Seen notes that a known tracker connected.
func (s *Store) Seen(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE trackers SET last_seen_at = ? WHERE id = ?`, ms(s.now()), id)
	return err
}

// Knock records that a tracker connected and returns it. An unknown IMEI is
// remembered as waiting.
func (s *Store) Knock(ctx context.Context, imei string) (Tracker, error) {
	return s.KnockLimited(ctx, imei, 0)
}

// KnockLimited is Knock that keeps at most maxWaiting waiting trackers
// (0 = no limit): a new one pushes out the one that tried least recently,
// so a flood of made-up IMEIs can't fill the list or the database.
func (s *Store) KnockLimited(ctx context.Context, imei string, maxWaiting int) (Tracker, error) {
	if t, err := s.TrackerByIMEI(ctx, imei); err == nil {
		t.LastSeenAt = fromMS(ms(s.now()))
		return t, s.Seen(ctx, t.ID)
	} else if err != ErrNotFound {
		return Tracker{}, err
	}
	now := ms(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Tracker{}, err
	}
	defer tx.Rollback()
	if maxWaiting > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM trackers WHERE id IN (
			SELECT id FROM trackers WHERE status = 'waiting' ORDER BY last_seen_at DESC, id DESC LIMIT -1 OFFSET ?)`, maxWaiting-1); err != nil {
			return Tracker{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO trackers (imei, public_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (imei) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		imei, RandomToken("dv_", 6), now, now); err != nil {
		return Tracker{}, err
	}
	if err := tx.Commit(); err != nil {
		return Tracker{}, err
	}
	return s.TrackerByIMEI(ctx, imei)
}

// AddTracker makes a waiting tracker active under a name.
func (s *Store) AddTracker(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE trackers SET status = 'active', name = ?, added_at = ? WHERE id = ? AND status = 'waiting'`, name, ms(s.now()), id)
	return s.changed(affected(res, err))
}

// RenameTracker changes an active tracker's name.
func (s *Store) RenameTracker(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE trackers SET name = ? WHERE id = ? AND status = 'active'`, name, id)
	return s.changed(affected(res, err))
}

// DeleteTracker forgets a tracker and its positions. If it connects again it
// shows up as waiting.
func (s *Store) DeleteTracker(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM trackers WHERE id = ?`, id)
	return s.changed(affected(res, err))
}

// NumberTrackers gives every active tracker without a Loxone number the
// next one (max + 1), in the order they were added. A removed tracker gives
// its number up.
func (s *Store) NumberTrackers(ctx context.Context) error {
	return s.number(ctx, "trackers", "status = 'active'", "added_at, id")
}

// number hands out loxone_number = max + 1 to the unnumbered rows of table.
func (s *Store) number(ctx context.Context, table, where, order string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(loxone_number), 0) FROM `+table).Scan(&next); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM `+table+` WHERE loxone_number IS NULL AND `+where+` ORDER BY `+order)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		next++
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET loxone_number = ? WHERE id = ?`, next, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
