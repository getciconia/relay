package tracker

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func record(io map[string]IOEntry) *Stored {
	return &Stored{
		FixTime: now.Add(-47 * time.Second), ReceivedAt: now.Add(-45 * time.Second),
		Latitude: 52.2296756, Longitude: 21.0122287, Altitude: 112, Speed: 42, Satellites: 9, Angle: 90,
		IO: io,
	}
}

func TestATM700Mapping(t *testing.T) {
	p := FromStored(record(map[string]IOEntry{
		"io113": {Size: 1, Value: 87}, "io21": {Size: 1, Value: 4}, "io241": {Size: 4, Value: 26003},
		"io70": {Size: 2, Value: 0xFFFF - 49}, "io182": {Size: 2, Value: 12}, "io69": {Size: 1, Value: 1},
	}), now)

	check := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %v", name, deref(got), want)
		}
	}
	check("latitude", p.Latitude, 52.2296756)
	check("speed", p.Speed, 42)
	check("battery", p.BatteryLevel, 87)
	check("temperature", p.ChipTemperature, -5)
	check("hdop", p.HDOP, 1.2)
	if *p.GSMSignal != 4 || *p.GSMOperatorCode != 26003 || *p.Satellites != 9 || *p.GPSSignal != 4 {
		t.Errorf("gsm %d operator %d sats %d gps %d", *p.GSMSignal, *p.GSMOperatorCode, *p.Satellites, *p.GPSSignal)
	}
	if *p.FixAgeSeconds != 47 || *p.FixedTime != "2026-10-31T11:59:13Z" || *p.ServerTime != "2026-10-31T11:59:15Z" {
		t.Errorf("times %s %d %s", *p.FixedTime, *p.FixAgeSeconds, *p.ServerTime)
	}
	if p.Attributes["io113"] != uint64(87) || p.Attributes["sat"] != 9 {
		t.Errorf("attributes %v", p.Attributes)
	}
}

func TestNoFixAndMissingValues(t *testing.T) {
	rec := record(map[string]IOEntry{"io69": {Size: 1, Value: 2}})
	p := FromStored(rec, now)
	if p.BatteryLevel != nil || p.HDOP != nil || p.ChipTemperature != nil {
		t.Errorf("missing values filled: %+v", p)
	}
	if *p.GPSSignal != 0 {
		t.Errorf("GNSS without fix gives gps_signal %d", *p.GPSSignal)
	}
	rec.Latitude, rec.Longitude = 0, 0
	if FromStored(rec, now).Located() {
		t.Error("0,0 counts as a position")
	}
	if e := FromStored(nil, now); e.FixedTime != nil || e.Attributes == nil {
		t.Errorf("empty position %+v", e)
	}
}

func TestGPSSignalBands(t *testing.T) {
	for hdop, want := range map[float64]int64{0.9: 5, 1: 5, 1.5: 4, 3: 3, 7: 2, 15: 1, 25: 0} {
		if got := GPSSignal(&hdop, true); *got != want {
			t.Errorf("hdop %v → %d, want %d", hdop, *got, want)
		}
	}
	if GPSSignal(nil, true) != nil {
		t.Error("no hdop should be nil")
	}
	if *GPSSignal(nil, false) != 0 {
		t.Error("no fix should be 0")
	}
}

func TestStatus(t *testing.T) {
	home := Area{Name: "Home", Latitude: 52.2296756, Longitude: 21.0122287, Radius: 100}
	town := Area{Name: "Town", Latitude: 52.23, Longitude: 21.01, Radius: 5000}
	areas := []Area{town, home}

	moving := FromStored(record(nil), now)
	if s := StatusOf(moving, areas); s.Key != Moving || s.Badge() != "Moving" {
		t.Errorf("moving: %+v", s)
	}
	rec := record(nil)
	rec.Speed = 0
	if s := StatusOf(FromStored(rec, now), areas); s.Badge() != "At Home" {
		t.Errorf("smallest area should win: %s", s.Badge())
	}
	rec.Latitude = 50
	if s := StatusOf(FromStored(rec, now), areas); s.Badge() != "Not moving" {
		t.Errorf("outside: %s", s.Badge())
	}
	rec.FixTime = now.Add(-25 * time.Hour)
	rec.Speed = 50
	if s := StatusOf(FromStored(rec, now), areas); s.Key != Offline {
		t.Errorf("old fix: %s", s.Key)
	}
	if s := StatusOf(Empty(), areas); s.Badge() != "No fix yet" {
		t.Errorf("empty: %s", s.Badge())
	}
}

func TestAgo(t *testing.T) {
	for secs, want := range map[int64]string{5: "5 s ago", 125: "2 min ago", 7300: "2 h ago", 90000: "1 day ago", 300000: "3 days ago"} {
		if got := Ago(secs); got != want {
			t.Errorf("%d → %q, want %q", secs, got, want)
		}
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
