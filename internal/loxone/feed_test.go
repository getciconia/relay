package loxone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getciconia/relay/internal/tracker"
)

// The cloud backend checks the same files (test/services/loxone_feed_golden_test.rb).
const golden = "../../testdata/loxone"

type scenario struct {
	Now     time.Time `json:"now"`
	FeedURL string    `json:"feed_url"`
	Areas   []struct {
		Number    int     `json:"number"`
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Radius    int     `json:"radius"`
	} `json:"areas"`
	Trackers []struct {
		Number   int               `json:"number"`
		Name     string            `json:"name"`
		Position *tracker.Position `json:"position"`
	} `json:"trackers"`
}

func load(t *testing.T) (scenario, Feed) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(golden, "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s scenario
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	var f Feed
	for _, a := range s.Areas {
		f.Areas = append(f.Areas, tracker.Area{Name: a.Name, Latitude: a.Latitude, Longitude: a.Longitude, Radius: a.Radius, LoxoneNumber: a.Number})
	}
	for _, tr := range s.Trackers {
		p := tracker.Empty()
		if tr.Position != nil {
			p = *tr.Position
		}
		f.Entries = append(f.Entries, tracker.Entry{Name: tr.Name, LoxoneNumber: tr.Number, Position: p, Status: tracker.StatusOf(p, f.Areas)})
	}
	return s, f
}

func compare(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join(golden, name))
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := range max(len(gl), len(wl)) {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("%s line %d:\n got  %q\n want %q", name, i+1, g, w)
		}
	}
}

func TestFeedMatchesTheCloud(t *testing.T) {
	_, f := load(t)
	compare(t, "feed.txt", f.Values())
}

func TestTemplateMatchesTheCloud(t *testing.T) {
	s, f := load(t)
	compare(t, "template.xml", f.Template(s.FeedURL, s.Now))
}

func TestSignatureFollowsNamesNotValues(t *testing.T) {
	_, f := load(t)
	before := f.Signature()
	f.Entries[0].Position = tracker.Empty()
	if f.Signature() != before {
		t.Error("signature changed with a position")
	}
	f.Areas[0].Name = "Home sweet home"
	if f.Signature() == before {
		t.Error("signature ignored a rename")
	}
}

func TestValueOfOneInput(t *testing.T) {
	_, f := load(t)
	car := f.Entries[0]
	ins := f.TrackerInputs(car)
	if ins[0].Title != "Dad's Car area" || f.Value(car, ins[0]) != "1" {
		t.Errorf("first input %+v = %q", ins[0], f.Value(car, ins[0]))
	}
	if ins[1].Area == nil || f.Value(car, ins[1]) != "1" {
		t.Errorf("per-area input %+v", ins[1])
	}
	if v := f.Value(f.Entries[3], ins[0]); v != "" {
		t.Errorf("a tracker without a position sends %q", v)
	}
}

func TestRoundHalfUpLikeRuby(t *testing.T) {
	for in, want := range map[float64]float64{23.45: 23.5, 23.44: 23.4, -3.25: -3.3, 0.05: 0.1, 2.675: 2.68} {
		digits := 1
		if in == 2.675 {
			digits = 2
		}
		if got := tracker.RoundHalfUp(in, digits); got != want {
			t.Errorf("%v → %v, want %v", in, got, want)
		}
	}
}
