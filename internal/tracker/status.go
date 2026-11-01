package tracker

import (
	"fmt"
	"math"
	"time"
)

// Area is a circle on the map, numbered for Loxone.
type Area struct {
	ID           int64
	Name         string
	Latitude     float64
	Longitude    float64
	Radius       int // metres
	Color        string
	LoxoneNumber int
}

const earthRadiusM = 6371000.0

// DistanceTo is the great-circle distance from the area's centre, in metres.
func (a Area) DistanceTo(lat, lng float64) float64 {
	phi1, phi2 := a.Latitude*math.Pi/180, lat*math.Pi/180
	dPhi := phi2 - phi1
	dLambda := (lng - a.Longitude) * math.Pi / 180
	h := math.Pow(math.Sin(dPhi/2), 2) + math.Cos(phi1)*math.Cos(phi2)*math.Pow(math.Sin(dLambda/2), 2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// Contains says whether the point is inside the area.
func (a Area) Contains(lat, lng float64) bool { return a.DistanceTo(lat, lng) <= float64(a.Radius) }

// Smallest is the smallest area containing the point (the first one wins a
// tie), or nil.
func Smallest(areas []Area, lat, lng float64) *Area {
	var best *Area
	for i := range areas {
		if areas[i].Contains(lat, lng) && (best == nil || areas[i].Radius < best.Radius) {
			best = &areas[i]
		}
	}
	return best
}

// When a tracker counts as offline or moving, as in the cloud.
const (
	OfflineAfter      = 24 * time.Hour
	MovingMinSpeedKMH = 5
	MovingMaxFixAge   = 10 * time.Minute
)

// State keys.
const (
	NoFix   = "no_fix"
	Moving  = "moving"
	Stopped = "stopped"
	Offline = "offline"
)

// Status is a tracker's state, worked out from its latest position.
type Status struct {
	Key  string
	Area *Area // the area a stopped tracker stands in
}

// StatusOf works out the state from a position and the areas.
func StatusOf(p Position, areas []Area) Status {
	switch {
	case p.FixedTime == nil || p.FixAgeSeconds == nil:
		return Status{Key: NoFix}
	case time.Duration(*p.FixAgeSeconds)*time.Second > OfflineAfter:
		return Status{Key: Offline}
	case p.Speed != nil && *p.Speed >= MovingMinSpeedKMH && time.Duration(*p.FixAgeSeconds)*time.Second <= MovingMaxFixAge:
		return Status{Key: Moving}
	}
	s := Status{Key: Stopped}
	if p.Latitude != nil && p.Longitude != nil {
		s.Area = Smallest(areas, *p.Latitude, *p.Longitude)
	}
	return s
}

// Located says whether the position has coordinates.
func (p Position) Located() bool { return p.Latitude != nil && p.Longitude != nil }

// Badge is the short state shown next to a tracker.
func (s Status) Badge() string {
	switch s.Key {
	case NoFix:
		return "No fix yet"
	case Moving:
		return "Moving"
	case Offline:
		return "Offline"
	}
	if s.Area != nil {
		return "At " + s.Area.Name
	}
	return "Not moving"
}

// Ago is "12 s ago", "5 min ago", "3 h ago" or "2 days ago".
func Ago(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s ago", max(seconds, 0))
	case seconds < 3600:
		return fmt.Sprintf("%d min ago", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%d h ago", seconds/3600)
	case seconds < 2*86400:
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", seconds/86400)
}
