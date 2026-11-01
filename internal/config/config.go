// Package config reads the relay's settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the relay can be told.
type Config struct {
	TrackerAddr    string        // RELAY_TRACKER_ADDR: Codec 8 over TCP, public
	TrackerTLSAddr string        // RELAY_TRACKER_TLS_ADDR: Codec 8 over TLS, public; "off" turns it off
	TLSCert        string        // RELAY_TLS_CERT: certificate file (PEM) for the TLS port; empty = self-signed
	TLSKey         string        // RELAY_TLS_KEY: its key file
	TLSHosts       []string      // RELAY_TLS_HOSTS: names / IPs for the self-signed certificate, comma-separated
	HTTPAddr       string        // RELAY_HTTP_ADDR: panel, Home Assistant API, Loxone feed; keep it private
	DBPath         string        // RELAY_DB: the SQLite file
	Retention      time.Duration // RELAY_RETENTION_DAYS: how long positions are kept
	LogPackets     bool          // RELAY_LOG_PACKETS: log every packet as hex
	Debug          bool          // RELAY_DEBUG: more logging
	MapTiles       string        // RELAY_MAP_TILES: map tile URL template for the panel; default OpenStreetMap

	// Addresses, when the relay can't tell them from the request (e.g. as a
	// Home Assistant app, where the panel is reached through Home Assistant).
	PanelURL   string // RELAY_PANEL_URL: the panel's address, for the link Home Assistant shows
	ApproveURL string // RELAY_APPROVE_URL: the exact link Home Assistant shows for approving (overrides RELAY_PANEL_URL)
	HAURL      string // RELAY_HA_URL: the address the Home Assistant integration uses for the relay
	LoxoneURL  string // RELAY_LOXONE_URL: the address the Miniserver uses for the relay
	LoxonePort string // RELAY_LOXONE_PORT: as a Home Assistant app, the host port the panel port is published on

	// Running as a Home Assistant app (RELAY_HA_APP=true, set by the app's
	// run.sh): the panel leaves out Loxone (its feed and template still
	// work) and links each tracker to its device in Home Assistant, which it
	// finds with the Supervisor's token (SUPERVISOR_TOKEN, set by Home
	// Assistant for an app with homeassistant_api).
	HAApp           bool
	SupervisorToken string

	// Limits on the public ports.
	MaxConns      int // RELAY_MAX_CONNECTIONS
	MaxConnsPerIP int // RELAY_MAX_CONNECTIONS_PER_IP
	MaxWaiting    int // RELAY_MAX_WAITING: unknown trackers kept in the waiting list
	NewPerIPHour  int // RELAY_NEW_TRACKERS_PER_IP_HOUR: unknown IMEIs one address may introduce per hour

	// MQTT with Home Assistant discovery; off while MQTTURL is empty.
	MQTTURL             string // RELAY_MQTT_URL: mqtt://host:1883 or mqtts://host:8883
	MQTTUsername        string // RELAY_MQTT_USERNAME
	MQTTPassword        string // RELAY_MQTT_PASSWORD
	MQTTTopic           string // RELAY_MQTT_TOPIC: base topic, default "ciconia"
	MQTTDiscoveryPrefix string // RELAY_MQTT_DISCOVERY_PREFIX: default "homeassistant"
	DeviceURL           string // RELAY_DEVICE_URL: link on each tracker's device page in Home Assistant; default RELAY_PANEL_URL
}

// Load reads the environment.
func Load() (Config, error) {
	c := Config{
		TrackerAddr:    env("RELAY_TRACKER_ADDR", ":5027"),
		TrackerTLSAddr: env("RELAY_TRACKER_TLS_ADDR", ":5028"),
		TLSCert:        os.Getenv("RELAY_TLS_CERT"),
		TLSKey:         os.Getenv("RELAY_TLS_KEY"),
		HTTPAddr:       env("RELAY_HTTP_ADDR", ":8080"),
		DBPath:         env("RELAY_DB", "relay.db"),
		MapTiles:       os.Getenv("RELAY_MAP_TILES"),
		PanelURL:       os.Getenv("RELAY_PANEL_URL"),
		ApproveURL:     os.Getenv("RELAY_APPROVE_URL"),
		HAURL:          os.Getenv("RELAY_HA_URL"),
		LoxoneURL:      os.Getenv("RELAY_LOXONE_URL"),
		LoxonePort:     os.Getenv("RELAY_LOXONE_PORT"),

		MQTTURL:             os.Getenv("RELAY_MQTT_URL"),
		MQTTUsername:        os.Getenv("RELAY_MQTT_USERNAME"),
		MQTTPassword:        os.Getenv("RELAY_MQTT_PASSWORD"),
		MQTTTopic:           env("RELAY_MQTT_TOPIC", "ciconia"),
		MQTTDiscoveryPrefix: env("RELAY_MQTT_DISCOVERY_PREFIX", "homeassistant"),
		DeviceURL:           env("RELAY_DEVICE_URL", os.Getenv("RELAY_PANEL_URL")),
	}
	if strings.EqualFold(c.TrackerTLSAddr, "off") {
		c.TrackerTLSAddr = ""
	}
	for _, h := range strings.Split(os.Getenv("RELAY_TLS_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.TLSHosts = append(c.TLSHosts, h)
		}
	}
	days, err := number("RELAY_RETENTION_DAYS", 7)
	if err != nil {
		return c, err
	}
	c.Retention = time.Duration(days) * 24 * time.Hour
	for _, n := range []struct {
		key string
		def int
		to  *int
	}{
		{"RELAY_MAX_CONNECTIONS", 500, &c.MaxConns},
		{"RELAY_MAX_CONNECTIONS_PER_IP", 20, &c.MaxConnsPerIP},
		{"RELAY_MAX_WAITING", 50, &c.MaxWaiting},
		{"RELAY_NEW_TRACKERS_PER_IP_HOUR", 10, &c.NewPerIPHour},
	} {
		if *n.to, err = number(n.key, n.def); err != nil {
			return c, err
		}
	}
	if c.LogPackets, err = boolean("RELAY_LOG_PACKETS"); err != nil {
		return c, err
	}
	if c.HAApp, err = boolean("RELAY_HA_APP"); err != nil {
		return c, err
	}
	c.SupervisorToken = os.Getenv("SUPERVISOR_TOKEN")
	if c.Debug, err = boolean("RELAY_DEBUG"); err != nil {
		return c, err
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func number(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a whole number, at least 1", key)
	}
	return n, nil
}

func boolean(key string) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return b, nil
}
