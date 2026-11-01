package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/getciconia/relay/codec8"
	"github.com/getciconia/relay/internal/tracker"
)

var ctx = context.Background()

func open(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knock(ctx, "356307042441013"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if w, _ := s.WaitingTrackers(ctx); len(w) != 1 {
		t.Fatalf("waiting after reopen: %v", w)
	}
}

func TestTrackerLifecycle(t *testing.T) {
	s, now := open(t)
	tr, err := s.Knock(ctx, "356307042441013")
	if err != nil || tr.Status != Waiting || len(tr.PublicID) != 15 {
		t.Fatalf("knock: %+v %v", tr, err)
	}
	*now = now.Add(time.Minute)
	again, _ := s.Knock(ctx, "356307042441013")
	if again.ID != tr.ID || again.PublicID != tr.PublicID || !again.LastSeenAt.Equal(*now) {
		t.Errorf("second knock changed identity or kept old time: %+v", again)
	}
	if err := s.AddTracker(ctx, tr.ID, "Car"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTracker(ctx, tr.ID, "Car"); err != ErrNotFound {
		t.Errorf("adding twice: %v", err)
	}
	active, _ := s.ActiveTrackers(ctx)
	if len(active) != 1 || active[0].Name != "Car" {
		t.Fatalf("active %+v", active)
	}
	if _, err := s.ActiveTrackerByPublicID(ctx, tr.PublicID); err != nil {
		t.Errorf("by public id: %v", err)
	}
	if err := s.DeleteTracker(ctx, tr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveTrackerByPublicID(ctx, tr.PublicID); err != ErrNotFound {
		t.Errorf("deleted tracker still found: %v", err)
	}
}

func TestPositionsLatestAndPrune(t *testing.T) {
	s, now := open(t)
	tr, _ := s.Knock(ctx, "1")
	s.AddTracker(ctx, tr.ID, "Car")
	other, _ := s.Knock(ctx, "2")
	s.AddTracker(ctx, other.ID, "Bike")

	rec := func(min int, lat float64) codec8.Record {
		return codec8.Record{Time: now.Add(time.Duration(min) * time.Minute), Latitude: lat, Longitude: 21, Satellites: 7,
			IO: map[uint16]codec8.IOValue{113: codec8.U8(80), 385: {Bytes: []byte{0xab}}}}
	}
	// Out of order: a buffered older record arrives after a newer one.
	if err := s.SavePositions(ctx, tr.ID, []codec8.Record{rec(0, 52.1), rec(-5, 52.0)}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestPosition(ctx, tr.ID)
	if err != nil || latest.Latitude != 52.1 || latest.IO["io113"].Value != 80 || latest.IO["io385"].Hex != "ab" {
		t.Fatalf("latest %+v %v", latest, err)
	}
	if p, _ := s.LatestPosition(ctx, other.ID); p != nil {
		t.Errorf("tracker without positions: %+v", p)
	}
	s.SavePositions(ctx, other.ID, []codec8.Record{rec(0, 50)})

	*now = now.Add(8 * 24 * time.Hour)
	s.SavePositions(ctx, tr.ID, []codec8.Record{rec(8*24*60, 52.2)})
	n, err := s.Prune(ctx, 7*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if c, _ := s.PositionCount(ctx, tr.ID); c != 1 {
		t.Errorf("car keeps %d positions", c)
	}
	if c, _ := s.PositionCount(ctx, other.ID); c != 1 {
		t.Errorf("the quiet bike's last position must stay, has %d", c)
	}
}

func TestNumbering(t *testing.T) {
	s, now := open(t)
	var ids []int64
	for _, imei := range []string{"1", "2", "3"} {
		tr, _ := s.Knock(ctx, imei)
		*now = now.Add(time.Second)
		s.AddTracker(ctx, tr.ID, "T"+imei)
		ids = append(ids, tr.ID)
	}
	waiting, _ := s.Knock(ctx, "9")
	if err := s.NumberTrackers(ctx); err != nil {
		t.Fatal(err)
	}
	s.DeleteTracker(ctx, ids[2]) // the highest number is given up
	s.NumberTrackers(ctx)
	tr4, _ := s.Knock(ctx, "4")
	*now = now.Add(time.Second)
	s.AddTracker(ctx, tr4.ID, "T4")
	s.NumberTrackers(ctx)
	got := map[string]int{}
	active, _ := s.ActiveTrackers(ctx)
	for _, t := range active {
		got[t.Name] = t.LoxoneNumber
	}
	if got["T1"] != 1 || got["T2"] != 2 || got["T4"] != 3 {
		t.Errorf("numbers %v", got)
	}
	if w, _ := s.TrackerByID(ctx, waiting.ID); w.LoxoneNumber != 0 {
		t.Errorf("waiting tracker numbered: %d", w.LoxoneNumber)
	}

	for _, name := range []string{"Home", "Work"} {
		*now = now.Add(time.Second)
		s.CreateArea(ctx, tracker.Area{Name: name, Latitude: 52, Longitude: 21, Radius: 100, Color: s.NextAreaColor(ctx)})
	}
	s.NumberAreas(ctx)
	areas, _ := s.Areas(ctx)
	if len(areas) != 2 || areas[0].Name != "Home" || areas[0].LoxoneNumber != 1 || areas[1].LoxoneNumber != 2 || areas[1].Color != AreaColors[1] {
		t.Errorf("areas %+v", areas)
	}
}

func TestLinking(t *testing.T) {
	s, now := open(t)
	req, deviceCode, err := s.IssueLink(ctx, "  My   Home  ")
	if err != nil || req.ClientName != "My Home" || len(req.UserCode) != 8 {
		t.Fatalf("issue %+v %v", req, err)
	}
	if r, _ := s.PollLink(ctx, deviceCode); r.Status != "pending" {
		t.Errorf("before approval: %s", r.Status)
	}
	found, err := s.PendingLink(ctx, req.DisplayCode())
	if err != nil || found.ID != req.ID {
		t.Fatalf("pending by typed code: %v", err)
	}
	if err := s.DecideLink(ctx, req.ID, true); err != nil {
		t.Fatal(err)
	}
	r, err := s.PollLink(ctx, deviceCode)
	if err != nil || r.Status != "approved" || r.Token == "" {
		t.Fatalf("approved poll %+v %v", r, err)
	}
	if again, _ := s.PollLink(ctx, deviceCode); again.Status != "expired" || again.Token != "" {
		t.Errorf("token handed out twice: %+v", again)
	}
	ok, err := s.Authenticate(ctx, r.Token)
	if err != nil || !ok {
		t.Fatalf("token rejected: %v", err)
	}
	if ok, _ := s.Authenticate(ctx, "cir_wrong"); ok {
		t.Error("wrong token accepted")
	}
	conns, _ := s.Connections(ctx)
	if len(conns) != 1 || conns[0].Name != "My Home" || conns[0].LastUsedAt == nil {
		t.Fatalf("connections %+v", conns)
	}
	s.DeleteConnection(ctx, conns[0].ID)
	if ok, _ := s.Authenticate(ctx, r.Token); ok {
		t.Error("disconnected token still works")
	}

	denied, code2, _ := s.IssueLink(ctx, "")
	s.DecideLink(ctx, denied.ID, false)
	if r, _ := s.PollLink(ctx, code2); r.Status != "denied" {
		t.Errorf("denied: %s", r.Status)
	}
	late, code3, _ := s.IssueLink(ctx, "")
	*now = now.Add(11 * time.Minute)
	if _, err := s.PendingLink(ctx, late.UserCode); err != ErrNotFound {
		t.Error("expired code still pending")
	}
	if r, _ := s.PollLink(ctx, code3); r.Status != "expired" {
		t.Errorf("expired: %s", r.Status)
	}
	if _, err := s.PollLink(ctx, "nope"); err != ErrNotFound {
		t.Errorf("unknown device code: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := open(t)
	v, err := s.Setting(ctx, "loxone_token", func() string { return "cil_a" })
	if err != nil || v != "cil_a" {
		t.Fatal(v, err)
	}
	v, _ = s.Setting(ctx, "loxone_token", func() string { return "cil_b" })
	if v != "cil_a" {
		t.Errorf("setting regenerated: %s", v)
	}
	if _, err := s.Setting(ctx, "missing", nil); err != ErrNotFound {
		t.Errorf("missing setting: %v", err)
	}
	s.SetSetting(ctx, "loxone_token", "cil_c")
	if v, _ := s.Setting(ctx, "loxone_token", nil); v != "cil_c" {
		t.Errorf("overwrite: %s", v)
	}
}

func TestWaitingListIsCapped(t *testing.T) {
	s, now := open(t)
	added, _ := s.Knock(ctx, "100000000000000")
	s.AddTracker(ctx, added.ID, "Mine")
	for i := range 5 {
		*now = now.Add(time.Second)
		if _, err := s.KnockLimited(ctx, "20000000000000"+string(rune('0'+i)), 3); err != nil {
			t.Fatal(err)
		}
	}
	waiting, _ := s.WaitingTrackers(ctx)
	if len(waiting) != 3 || waiting[0].IMEI != "200000000000004" || waiting[2].IMEI != "200000000000002" {
		t.Fatalf("kept %+v", waiting)
	}
	if active, _ := s.ActiveTrackers(ctx); len(active) != 1 {
		t.Error("an added tracker was pushed out")
	}
	// A known one knocking again doesn't push anything out.
	*now = now.Add(time.Second)
	s.KnockLimited(ctx, "200000000000002", 3)
	if waiting, _ := s.WaitingTrackers(ctx); len(waiting) != 3 || waiting[0].IMEI != "200000000000002" {
		t.Errorf("after a known knock: %+v", waiting)
	}
}
