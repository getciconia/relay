// Package loxone builds what a Loxone Miniserver reads through a Virtual
// HTTP Input, and the template that sets that input up. It matches the
// cloud's LoxoneFeed line for line (checked against testdata/loxone), so
// the same docs and Loxone programs work with either.
//
// Loxone's Command Recognition only extracts numbers, so the feed is plain
// text, one key=value per line:
//
//	t1_zone=2          (number of the area it's in, 0 = none)
//	t1_z2_in=1         (in area 2: 1/0)
//	t1_z2_distance=35  (metres from that area's centre)
//
// Keys use numbers given once per tracker and area, never names, so
// renaming keeps Loxone working. A value that's unknown is left out and the
// Miniserver keeps the last one.
package loxone

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/getciconia/relay/internal/tracker"
)

// PollingSeconds is how often the Miniserver asks (Loxone's minimum).
const PollingSeconds = 10

const (
	limit           = 1_000_000_000
	batteryLowBelow = 20 // %
)

// Field is one value of every tracker (or of every tracker and area).
type Field struct {
	Key     string // after "tN_" or "tN_zM_"
	Title   string // %{tracker} / %{area} filled in
	Range   string // what the number can be
	Unit    string // Loxone's format
	Hint    string // the input's comment in Loxone Config
	PerArea bool
}

// Fields is every value, in template order. Same as LoxoneFeed::FIELDS.
var Fields = []Field{
	{Key: "zone", Title: "%{tracker} area", Range: "area number, 0 = none", Unit: "<v>", Hint: "The area's number, 0 = none. If areas overlap, the smallest one."},
	{Key: "in", Title: "%{tracker} at %{area}", Range: "1 or 0", Unit: "<v>", Hint: "1 while in the area.", PerArea: true},
	{Key: "distance", Title: "%{tracker} distance to %{area}", Range: "metres", Unit: "<v> m", Hint: "Metres from the area's centre.", PerArea: true},
	{Key: "moving", Title: "%{tracker} moving", Range: "1 or 0", Unit: "<v>", Hint: "1 while moving."},
	{Key: "speed", Title: "%{tracker} speed", Range: "km/h", Unit: "<v> km/h"},
	{Key: "battery", Title: "%{tracker} battery", Range: "0–100 %", Unit: "<v> %", Hint: "Only trackers that report a percentage."},
	{Key: "battery_low", Title: "%{tracker} battery low", Range: "1 or 0", Unit: "<v>", Hint: fmt.Sprintf("1 below %d %%.", batteryLowBelow)},
	{Key: "lat", Title: "%{tracker} latitude", Range: "degrees", Unit: "<v.6>°"},
	{Key: "lon", Title: "%{tracker} longitude", Range: "degrees", Unit: "<v.6>°"},
	{Key: "fix_age", Title: "%{tracker} position age", Range: "seconds", Unit: "<v> s", Hint: "Seconds since its position was measured."},
	{Key: "gps_signal", Title: "%{tracker} GPS signal", Range: "0–5", Unit: "<v>/5", Hint: "0–5, from how precise the fix is."},
	{Key: "satellites", Title: "%{tracker} satellites", Range: "count", Unit: "<v>"},
	{Key: "altitude", Title: "%{tracker} altitude", Range: "metres", Unit: "<v> m"},
	{Key: "gsm_signal", Title: "%{tracker} GSM signal", Range: "0–5", Unit: "<v>/5", Hint: "0–5."},
	{Key: "gsm_operator", Title: "%{tracker} GSM operator", Range: "network code", Unit: "<v>", Hint: "Network code (country + operator), e.g. 26003 = Orange Poland."},
	{Key: "temperature", Title: "%{tracker} temperature", Range: "°C", Unit: "<v.1> °C", Hint: "Inside the tracker, not the air around it."},
}

// Input is one input of the template.
type Input struct {
	Key, Title, Unit, Comment string
	Field                     Field
	Area                      *tracker.Area
}

// Feed is the trackers and areas a Miniserver reads, both in number order.
type Feed struct {
	Entries []tracker.Entry
	Areas   []tracker.Area
}

// Values is the text the Miniserver polls.
func (f Feed) Values() string {
	var b strings.Builder
	for _, e := range f.Entries {
		values := f.trackerValues(e)
		for _, field := range Fields {
			if field.PerArea {
				for _, a := range f.Areas {
					if v, ok := values[perArea(field.Key, a.LoxoneNumber)]; ok {
						fmt.Fprintf(&b, "t%d_z%d_%s=%s\n", e.LoxoneNumber, a.LoxoneNumber, field.Key, v)
					}
				}
			} else if v, ok := values[field.Key]; ok {
				fmt.Fprintf(&b, "t%d_%s=%s\n", e.LoxoneNumber, field.Key, v)
			}
		}
	}
	return b.String()
}

// Inputs is every input of the template, in order.
func (f Feed) Inputs() []Input {
	var out []Input
	for _, e := range f.Entries {
		out = append(out, f.TrackerInputs(e)...)
	}
	return out
}

// TrackerInputs is one tracker's inputs.
func (f Feed) TrackerInputs(e tracker.Entry) []Input {
	var out []Input
	for _, field := range Fields {
		if field.PerArea {
			for i := range f.Areas {
				a := &f.Areas[i]
				out = append(out, f.input(field, fmt.Sprintf("t%d_z%d_%s", e.LoxoneNumber, a.LoxoneNumber, field.Key), e.Name, a))
			}
		} else {
			out = append(out, f.input(field, fmt.Sprintf("t%d_%s", e.LoxoneNumber, field.Key), e.Name, nil))
		}
	}
	return out
}

func (f Feed) input(field Field, key, trackerName string, area *tracker.Area) Input {
	comment := field.Hint
	if field.Key == "zone" && len(f.Areas) > 0 {
		comment += " " + f.AreaLegend()
	}
	title := strings.ReplaceAll(field.Title, "%{tracker}", trackerName)
	if area != nil {
		title = strings.ReplaceAll(title, "%{area}", area.Name)
	}
	return Input{Key: key, Title: title, Unit: field.Unit, Comment: comment, Field: field, Area: area}
}

// AreaLegend is "1 = Home, 2 = Work", for a Status block.
func (f Feed) AreaLegend() string {
	parts := make([]string, len(f.Areas))
	for i, a := range f.Areas {
		parts[i] = fmt.Sprintf("%d = %s", a.LoxoneNumber, a.Name)
	}
	return strings.Join(parts, ", ")
}

// Signature changes whenever the template would: a tracker or area added,
// renamed or removed.
func (f Feed) Signature() string {
	lines := make([]string, 0)
	for _, in := range f.Inputs() {
		lines = append(lines, in.Key+"="+in.Title)
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Template is VI_Ciconia.xml: a Virtual HTTP Input reading valuesURL, one
// command per value.
func (f Feed) Template(valuesURL string, today time.Time) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	fmt.Fprintf(&b, `<VirtualInHttp Title="Ciconia" Comment="Ciconia GPS trackers, made %s" Address="%s" HintText="" PollingTime="%d">`+"\n",
		today.UTC().Format("2006-01-02"), escape(valuesURL), PollingSeconds)
	b.WriteString("\t" + `<Info templateType="2" minVersion="14051207"/>` + "\n")
	for _, in := range f.Inputs() {
		fmt.Fprintf(&b, "\t"+`<VirtualInHttpCmd Title="%s" Comment="%s" Check="%s=\v" Signed="true" Analog="true" `,
			escape(in.Title), escape(in.Comment), escape(in.Key))
		fmt.Fprintf(&b, `SourceValLow="0" DestValLow="0" SourceValHigh="100" DestValHigh="100" DefVal="0" MinVal="-%d" MaxVal="%d" Unit="%s" HintText=""/>`+"\n",
			limit, limit, escape(in.Unit))
	}
	b.WriteString("</VirtualInHttp>\n")
	return b.String()
}

// Value is what the Miniserver reads for one input right now, "" when it's
// left out.
func (f Feed) Value(e tracker.Entry, in Input) string {
	values := f.trackerValues(e)
	if in.Area != nil {
		return values[perArea(in.Field.Key, in.Area.LoxoneNumber)]
	}
	return values[in.Field.Key]
}

func perArea(key string, number int) string { return key + "@" + strconv.Itoa(number) }

// trackerValues is every value known for one tracker; missing = unknown.
func (f Feed) trackerValues(e tracker.Entry) map[string]string {
	p := e.Position
	v := map[string]string{}
	if p.BatteryLevel != nil && *p.BatteryLevel >= 0 && *p.BatteryLevel <= 100 {
		v["battery"] = itoa(math.Round(*p.BatteryLevel))
		v["battery_low"] = bit(*p.BatteryLevel < batteryLowBelow)
	}
	setInt(v, "gsm_signal", p.GSMSignal)
	setInt(v, "gsm_operator", p.GSMOperatorCode)
	if !p.Located() {
		return v
	}
	lat, lng := *p.Latitude, *p.Longitude
	zone := 0
	if a := tracker.Smallest(f.Areas, lat, lng); a != nil {
		zone = a.LoxoneNumber
	}
	v["zone"] = strconv.Itoa(zone)
	for _, a := range f.Areas {
		v[perArea("in", a.LoxoneNumber)] = bit(a.Contains(lat, lng))
		v[perArea("distance", a.LoxoneNumber)] = itoa(math.Round(a.DistanceTo(lat, lng)))
	}
	v["lat"] = fmt.Sprintf("%.6f", lat)
	v["lon"] = fmt.Sprintf("%.6f", lng)
	v["moving"] = bit(e.Status.Key == tracker.Moving)
	if p.Speed != nil {
		v["speed"] = itoa(math.Round(*p.Speed))
	}
	setInt(v, "fix_age", p.FixAgeSeconds)
	setInt(v, "gps_signal", p.GPSSignal)
	setInt(v, "satellites", p.Satellites)
	if p.Altitude != nil {
		v["altitude"] = itoa(math.Round(*p.Altitude))
	}
	if p.ChipTemperature != nil {
		v["temperature"] = rubyFloat(tracker.RoundHalfUp(*p.ChipTemperature, 1))
	}
	return v
}

func setInt(v map[string]string, key string, n *int64) {
	if n != nil {
		v[key] = strconv.FormatInt(*n, 10)
	}
}

func itoa(f float64) string { return strconv.FormatInt(int64(f), 10) }

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// rubyFloat prints a float the way Ruby's Float#to_s does for ordinary
// values: shortest form, always with a decimal point ("23.0").
func rubyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// escape is ERB::Util.html_escape: & < > " '.
var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escape(s string) string { return escaper.Replace(s) }
