package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/getciconia/relay/internal/tracker"
)

// AreaColors are the colours an area can have, as in the cloud web app.
var AreaColors = []string{"#18bcf2", "#4caf50", "#7e57c2", "#ff9800", "#e4513a", "#009688"}

const areaColumns = `id, name, latitude, longitude, radius, color, COALESCE(loxone_number, 0)`

// Areas lists the areas in Loxone number order (unnumbered last).
func (s *Store) Areas(ctx context.Context) ([]tracker.Area, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+areaColumns+` FROM areas ORDER BY loxone_number IS NULL, loxone_number, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tracker.Area
	for rows.Next() {
		var a tracker.Area
		if err := rows.Scan(&a.ID, &a.Name, &a.Latitude, &a.Longitude, &a.Radius, &a.Color, &a.LoxoneNumber); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Area finds one area.
func (s *Store) Area(ctx context.Context, id int64) (tracker.Area, error) {
	var a tracker.Area
	err := s.db.QueryRowContext(ctx, `SELECT `+areaColumns+` FROM areas WHERE id = ?`, id).
		Scan(&a.ID, &a.Name, &a.Latitude, &a.Longitude, &a.Radius, &a.Color, &a.LoxoneNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// NextAreaColor is the colour a new area starts with.
func (s *Store) NextAreaColor(ctx context.Context) string {
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM areas`).Scan(&n)
	return AreaColors[n%len(AreaColors)]
}

// CreateArea adds an area and returns its id.
func (s *Store) CreateArea(ctx context.Context, a tracker.Area) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO areas (name, latitude, longitude, radius, color, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, a.Latitude, a.Longitude, a.Radius, a.Color, ms(s.now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateArea changes an area (its Loxone number stays).
func (s *Store) UpdateArea(ctx context.Context, a tracker.Area) error {
	res, err := s.db.ExecContext(ctx, `UPDATE areas SET name = ?, latitude = ?, longitude = ?, radius = ?, color = ? WHERE id = ?`,
		a.Name, a.Latitude, a.Longitude, a.Radius, a.Color, a.ID)
	return affected(res, err)
}

// DeleteArea removes an area.
func (s *Store) DeleteArea(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM areas WHERE id = ?`, id)
	return affected(res, err)
}

// NumberAreas gives every area without a Loxone number the next one.
func (s *Store) NumberAreas(ctx context.Context) error {
	return s.number(ctx, "areas", "1 = 1", "created_at, id")
}
