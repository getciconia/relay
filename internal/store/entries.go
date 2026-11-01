package store

import (
	"context"

	"github.com/getciconia/relay/internal/tracker"
)

// Entries is every added tracker with its latest position and state, in
// Loxone number order (trackers and areas get their numbers here first).
func (s *Store) Entries(ctx context.Context) ([]tracker.Entry, []tracker.Area, error) {
	if err := s.NumberTrackers(ctx); err != nil {
		return nil, nil, err
	}
	if err := s.NumberAreas(ctx); err != nil {
		return nil, nil, err
	}
	trackers, err := s.trackers(ctx, `status = 'active' ORDER BY loxone_number, id`)
	if err != nil {
		return nil, nil, err
	}
	areas, err := s.Areas(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	entries := make([]tracker.Entry, 0, len(trackers))
	for _, t := range trackers {
		latest, err := s.LatestPosition(ctx, t.ID)
		if err != nil {
			return nil, nil, err
		}
		p := tracker.FromStored(latest, now)
		entries = append(entries, tracker.Entry{
			ID: t.ID, PublicID: t.PublicID, Name: t.Name, Model: t.Model, LoxoneNumber: t.LoxoneNumber,
			Position: p, Status: tracker.StatusOf(p, areas),
		})
	}
	return entries, areas, nil
}
