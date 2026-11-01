package mqtt

import (
	"math"

	"github.com/getciconia/relay/internal/tracker"
)

// sensor is one Home Assistant sensor read from the state topic. Names, units
// and categories match the Ciconia integration's sensors.
type sensor struct {
	key         string // also the field in the state JSON
	name        string
	deviceClass string
	unit        string
	stateClass  string
	diagnostic  bool
	disabled    bool // off until the user turns it on
	icon        string
	precision   *int // decimals Home Assistant shows
}

var sensors = []sensor{
	{key: "battery_level", name: "Battery", deviceClass: "battery", unit: "%", stateClass: "measurement"},
	{key: "speed", name: "Speed", deviceClass: "speed", unit: "km/h", stateClass: "measurement", precision: decimals(0)},
	{key: "altitude", name: "Altitude", deviceClass: "distance", unit: "m", stateClass: "measurement", precision: decimals(0)},
	{key: "fixed_time", name: "Last fix", deviceClass: "timestamp"},
	{key: "fix_age_seconds", name: "Fix age", deviceClass: "duration", unit: "s", stateClass: "measurement", diagnostic: true, precision: decimals(0)},
	{key: "gsm_signal", name: "GSM signal", stateClass: "measurement", diagnostic: true, icon: "mdi:signal"},
	{key: "gps_signal", name: "GPS signal", stateClass: "measurement", diagnostic: true, icon: "mdi:crosshairs-gps"},
	{key: "satellites", name: "Visible satellites", stateClass: "measurement", diagnostic: true, icon: "mdi:satellite-variant"},
	{key: "chip_temperature", name: "Chip temperature", deviceClass: "temperature", unit: "°C", stateClass: "measurement", diagnostic: true, precision: decimals(1)},
	{key: "hdop", name: "HDOP", stateClass: "measurement", diagnostic: true, disabled: true, precision: decimals(1), icon: "mdi:crosshairs-question"},
	{key: "gsm_operator_code", name: "GSM operator code", diagnostic: true, disabled: true, icon: "mdi:radio-tower"},
}

func decimals(n int) *int { return &n }

// discovery is one tracker as a Home Assistant device: its location and
// sensors, all in one retained message.
func (p *Publisher) discovery(e tracker.Entry) map[string]any {
	id := "ciconia_" + e.PublicID
	device := map[string]any{
		"identifiers":  []string{id},
		"name":         e.Name,
		"manufacturer": "Ciconia",
	}
	if e.Model != "" {
		device["model"] = e.Model
	}
	if p.DeviceURL != "" {
		device["configuration_url"] = p.DeviceURL
	}
	components := map[string]any{
		"location": map[string]any{
			"platform":              "device_tracker",
			"unique_id":             id + "_location",
			"name":                  nil, // the device's own name
			"json_attributes_topic": p.locationTopic(e.PublicID),
			"source_type":           "gps",
			"icon":                  "mdi:map-marker",
		},
	}
	for _, s := range sensors {
		c := map[string]any{
			"platform":       "sensor",
			"unique_id":      id + "_" + s.key,
			"name":           s.name,
			"state_topic":    p.stateTopic(e.PublicID),
			"value_template": "{{ value_json." + s.key + " }}",
		}
		if s.deviceClass != "" {
			c["device_class"] = s.deviceClass
		}
		if s.unit != "" {
			c["unit_of_measurement"] = s.unit
		}
		if s.stateClass != "" {
			c["state_class"] = s.stateClass
		}
		if s.diagnostic {
			c["entity_category"] = "diagnostic"
		}
		if s.disabled {
			c["enabled_by_default"] = false
		}
		if s.icon != "" {
			c["icon"] = s.icon
		}
		if s.precision != nil {
			c["suggested_display_precision"] = *s.precision
		}
		components[s.key] = c
	}
	return map[string]any{
		"device": device,
		"origin": map[string]any{
			"name":        "Ciconia Private Relay",
			"sw_version":  p.Version,
			"support_url": "https://getciconia.com",
		},
		"availability_topic": p.availabilityTopic(),
		"qos":                1,
		"components":         components,
	}
}

// state is the tracker as the API returns it (device_id, name, model and the
// position fields), plus its status and the area it stands in.
func stateOf(e tracker.Entry) map[string]any {
	p := e.Position
	out := map[string]any{
		"device_id":         e.PublicID,
		"name":              e.Name,
		"model":             nilIfEmpty(e.Model),
		"status":            e.Status.Key,
		"area":              nil,
		"latitude":          p.Latitude,
		"longitude":         p.Longitude,
		"altitude":          p.Altitude,
		"speed":             p.Speed,
		"battery_level":     battery(p.BatteryLevel),
		"gsm_signal":        p.GSMSignal,
		"gsm_operator_code": p.GSMOperatorCode,
		"chip_temperature":  p.ChipTemperature,
		"satellites":        p.Satellites,
		"hdop":              p.HDOP,
		"gps_signal":        p.GPSSignal,
		"fixed_time":        p.FixedTime,
		"fix_age_seconds":   p.FixAgeSeconds,
		"device_time":       p.DeviceTime,
		"server_time":       p.ServerTime,
		"attributes":        p.Attributes,
	}
	if e.Status.Area != nil {
		out["area"] = e.Status.Area.Name
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// battery keeps only real percentages, like the integration.
func battery(v *float64) *float64 {
	if v == nil || *v < 0 || *v > 100 {
		return nil
	}
	return v
}

// gpsUERE is a typical GPS range error in metres: accuracy ≈ HDOP × this,
// as in the integration.
const gpsUERE = 5

// locationOf is what Home Assistant's device tracker reads: latitude,
// longitude and gps_accuracy set its location (and so its zone); the rest
// become attributes. Unknown fields are left out.
func locationOf(p tracker.Position) map[string]any {
	out := map[string]any{}
	if p.Latitude != nil && p.Longitude != nil {
		out["latitude"], out["longitude"] = *p.Latitude, *p.Longitude
		accuracy := 0
		if p.HDOP != nil {
			accuracy = int(math.Round(*p.HDOP * gpsUERE))
		}
		out["gps_accuracy"] = accuracy
	}
	if b := battery(p.BatteryLevel); b != nil {
		out["battery_level"] = int(math.Round(*b))
	}
	for key, v := range map[string]any{"altitude": p.Altitude, "speed": p.Speed, "fixed_time": p.FixedTime, "fix_age_seconds": p.FixAgeSeconds} {
		switch v := v.(type) {
		case *float64:
			if v != nil {
				out[key] = *v
			}
		case *string:
			if v != nil {
				out[key] = *v
			}
		case *int64:
			if v != nil {
				out[key] = *v
			}
		}
	}
	return out
}
