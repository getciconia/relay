package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/getciconia/relay/internal/codec8"
	"github.com/getciconia/relay/internal/tracker"
)

// SavePositions stores a packet's records for a tracker, all or nothing.
func (s *Store) SavePositions(ctx context.Context, trackerID int64, records []codec8.Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO positions
		(tracker_id, fix_time, received_at, latitude, longitude, altitude, angle, speed, satellites, priority, event_io, io)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	received := ms(s.now())
	for _, r := range records {
		io := make(map[string]tracker.IOEntry, len(r.IO))
		for id, v := range r.IO {
			e := tracker.IOEntry{Size: v.Size, Value: v.Raw}
			if v.Size == 0 {
				e.Hex = hex.EncodeToString(v.Bytes)
			}
			io["io"+strconv.Itoa(int(id))] = e
		}
		ioJSON, err := json.Marshal(io)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, trackerID, ms(r.Time), received, r.Latitude, r.Longitude,
			r.Altitude, r.Angle, r.Speed, r.Satellites, r.Priority, r.EventIO, string(ioJSON)); err != nil {
			return err
		}
	}
	return s.changed(tx.Commit())
}

// LatestPosition is the record with the newest fix time, or nil.
func (s *Store) LatestPosition(ctx context.Context, trackerID int64) (*tracker.Stored, error) {
	row := s.db.QueryRowContext(ctx, `SELECT fix_time, received_at, latitude, longitude, altitude, angle, speed, satellites, priority, event_io, io
		FROM positions WHERE tracker_id = ? ORDER BY fix_time DESC, id DESC LIMIT 1`, trackerID)
	var p tracker.Stored
	var fix, received int64
	var ioJSON string
	err := row.Scan(&fix, &received, &p.Latitude, &p.Longitude, &p.Altitude, &p.Angle, &p.Speed, &p.Satellites, &p.Priority, &p.EventIO, &ioJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.FixTime, p.ReceivedAt = fromMS(fix), fromMS(received)
	if err := json.Unmarshal([]byte(ioJSON), &p.IO); err != nil {
		return nil, err
	}
	return &p, nil
}

// PositionCount counts a tracker's stored positions.
func (s *Store) PositionCount(ctx context.Context, trackerID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM positions WHERE tracker_id = ?`, trackerID).Scan(&n)
	return n, err
}

// Prune deletes positions received more than keep ago, but always keeps each
// tracker's latest one, so a tracker that went quiet still shows where it was.
func (s *Store) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM positions
		WHERE received_at < ?
		  AND id NOT IN (
			SELECT (SELECT p2.id FROM positions p2 WHERE p2.tracker_id = t.id ORDER BY p2.fix_time DESC, p2.id DESC LIMIT 1)
			FROM trackers t
		  )`, ms(s.now().Add(-keep)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
