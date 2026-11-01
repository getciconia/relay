// Package tracker turns stored AVL records into the position fields the
// Ciconia API returns, and works out a tracker's state. It mirrors the cloud
// backend (its ATM700 mapping, GPS signal and tracker states), so Home
// Assistant and Loxone see the same values from either.
package tracker

import (
	"math"
	"strconv"
	"time"
)

// Stored is one AVL record as kept in the database.
type Stored struct {
	FixTime    time.Time
	ReceivedAt time.Time
	Latitude   float64
	Longitude  float64
	Altitude   int
	Angle      int
	Speed      int // km/h
	Satellites int
	Priority   int
	EventIO    int
	IO         map[string]IOEntry // "io113" → value
}

// IOEntry is one IO element: its number and how many bytes it had (0 for
// variable-length ones, kept as hex).
type IOEntry struct {
	Size  int    `json:"s"`
	Value uint64 `json:"v"`
	Hex   string `json:"h,omitempty"`
}

func (e IOEntry) signed() int64 {
	switch e.Size {
	case 1:
		return int64(int8(e.Value))
	case 2:
		return int64(int16(e.Value))
	case 4:
		return int64(int32(e.Value))
	default:
		return int64(e.Value)
	}
}

// Position is the API's normalized position: every field, nil when unknown.
type Position struct {
	Latitude        *float64       `json:"latitude"`
	Longitude       *float64       `json:"longitude"`
	Altitude        *float64       `json:"altitude"`
	Speed           *float64       `json:"speed"`
	BatteryLevel    *float64       `json:"battery_level"`
	GSMSignal       *int64         `json:"gsm_signal"`
	GSMOperatorCode *int64         `json:"gsm_operator_code"`
	ChipTemperature *float64       `json:"chip_temperature"`
	Satellites      *int64         `json:"satellites"`
	HDOP            *float64       `json:"hdop"`
	GPSSignal       *int64         `json:"gps_signal"`
	FixedTime       *string        `json:"fixed_time"`
	FixAgeSeconds   *int64         `json:"fix_age_seconds"`
	DeviceTime      *string        `json:"device_time"`
	ServerTime      *string        `json:"server_time"`
	Attributes      map[string]any `json:"attributes"`
}

// Empty is the position of a tracker that hasn't reported yet.
func Empty() Position { return Position{Attributes: map[string]any{}} }

// IO element numbers the ATM700 mapping reads.
const (
	ioGSMSignal   = 21  // 0–5
	ioGNSSStatus  = 69  // 0 off, 1 on with fix, 2 on without fix, 3 sleep
	ioTemperature = 70  // PCB temperature, 0.1 °C, signed
	ioBatteryPct  = 113 // battery level, %
	ioHDOP        = 182 // 0.1
	ioOperator    = 241 // active GSM operator, MCC+MNC
)

// FromStored maps a stored record of a Teltonika ATM700 (nil = no record).
func FromStored(s *Stored, now time.Time) Position {
	if s == nil {
		return Empty()
	}
	p := Position{Attributes: map[string]any{}}
	if s.Latitude != 0 || s.Longitude != 0 {
		p.Latitude, p.Longitude = ptr(s.Latitude), ptr(s.Longitude)
	}
	p.Altitude = ptr(float64(s.Altitude))
	p.Speed = ptr(float64(s.Speed))
	p.Satellites = ptr(int64(s.Satellites))

	io := func(id int) (IOEntry, bool) {
		e, ok := s.IO["io"+strconv.Itoa(id)]
		return e, ok && e.Size > 0
	}
	if e, ok := io(ioBatteryPct); ok {
		p.BatteryLevel = ptr(float64(e.Value))
	}
	if e, ok := io(ioGSMSignal); ok {
		p.GSMSignal = ptr(int64(e.Value))
	}
	if e, ok := io(ioOperator); ok {
		p.GSMOperatorCode = ptr(int64(e.Value))
	}
	if e, ok := io(ioTemperature); ok {
		p.ChipTemperature = ptr(RoundHalfUp(float64(e.signed())/10, 2))
	}
	if e, ok := io(ioHDOP); ok {
		p.HDOP = ptr(RoundHalfUp(float64(e.Value)/10, 1))
	}
	fix := s.Satellites != 0
	if e, ok := io(ioGNSSStatus); ok {
		fix = e.Value == 1
	}
	p.GPSSignal = GPSSignal(p.HDOP, fix)

	fixed := s.FixTime.UTC().Format(time.RFC3339)
	received := s.ReceivedAt.UTC().Format(time.RFC3339)
	p.FixedTime, p.DeviceTime, p.ServerTime = &fixed, &fixed, &received
	p.FixAgeSeconds = ptr(int64(math.Round(now.Sub(s.FixTime).Seconds())))

	p.Attributes["priority"] = s.Priority
	p.Attributes["event"] = s.EventIO
	p.Attributes["sat"] = s.Satellites
	p.Attributes["course"] = s.Angle
	for key, e := range s.IO {
		if e.Size == 0 {
			p.Attributes[key] = e.Hex
		} else {
			p.Attributes[key] = e.Value
		}
	}
	return p
}

// GPSSignal is fix quality 0–5 from HDOP, the usual DOP rating bands:
// 5 ideal (≤ 1), 4 excellent (≤ 2), 3 good (≤ 5), 2 moderate (≤ 10),
// 1 fair (≤ 20), 0 poor or no fix. Nil when the tracker reports no HDOP.
func GPSSignal(hdop *float64, fix bool) *int64 {
	if !fix {
		return ptr(int64(0))
	}
	if hdop == nil || *hdop <= 0 {
		return nil
	}
	for _, band := range []struct {
		limit float64
		level int64
	}{{1, 5}, {2, 4}, {5, 3}, {10, 2}, {20, 1}} {
		if *hdop <= band.limit {
			return ptr(band.level)
		}
	}
	return ptr(int64(0))
}

func ptr[T any](v T) *T { return &v }

// RoundHalfUp rounds like Ruby's Float#round(digits): half away from zero,
// corrected for binary representation, so 23.45 rounds to 23.5 as people
// expect (plain math.Round(23.45*10)/10 gives 23.4).
func RoundHalfUp(x float64, digits int) float64 {
	if x == 0 || digits <= 0 {
		return math.Round(x)
	}
	s := math.Pow(10, float64(digits))
	f := math.Round(x * s)
	if x > 0 {
		if (f+0.5)/s <= x {
			f++
		}
	} else if (f-0.5)/s >= x {
		f--
	}
	return f / s
}
