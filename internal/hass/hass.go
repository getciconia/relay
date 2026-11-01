// Package hass finds each tracker's device in Home Assistant, so the panel
// can link to its device page. It works when the relay runs as a Home
// Assistant app: the Supervisor gives the app a token for Home Assistant's
// API (homeassistant_api in the app's config).
//
// Home Assistant doesn't expose its device registry over the REST API, but
// it renders templates, and templates can read the registry. One template
// lists the devices of the MQTT and Ciconia integrations with their
// identifiers; a tracker's device is the one whose identifier holds its
// public ID: ("mqtt", "ciconia_<id>") from MQTT discovery, ("ciconia", "<id>")
// from the integration.
package hass

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SupervisorAPI is Home Assistant's API as an app reaches it.
const SupervisorAPI = "http://supervisor/core/api"

// deviceTemplate prints "domain|identifier|device id;" for every device of
// the two integrations.
const deviceTemplate = `{%- for d in (integration_entities('mqtt') + integration_entities('ciconia')) | map('device_id') | reject('none') | unique -%}
{%- for i in device_attr(d, 'identifiers') or [] -%}
{{ i[0] }}|{{ i[1] }}|{{ d }};
{%- endfor -%}
{%- endfor -%}`

// Devices looks the devices up, at most once per TTL.
type Devices struct {
	URL    string // Home Assistant's API, e.g. SupervisorAPI
	Token  string
	Log    *slog.Logger
	TTL    time.Duration // default 30 s
	Client *http.Client  // default: 5 s timeout

	mu      sync.Mutex
	cached  map[string]string
	fetched time.Time
	failed  bool
}

// All maps trackers' public IDs to their Home Assistant device IDs. It's
// empty when Home Assistant can't be asked; that's logged once.
func (d *Devices) All(ctx context.Context) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ttl := d.TTL
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	if d.cached != nil && time.Since(d.fetched) < ttl {
		return d.cached
	}
	m, err := d.fetch(ctx)
	if err != nil {
		if !d.failed && d.Log != nil {
			d.Log.Warn("can't look up trackers' devices in Home Assistant; the panel won't link to them", "err", err)
		}
		d.failed = true
		m = map[string]string{}
	} else if d.failed {
		d.failed = false
		if d.Log != nil {
			d.Log.Info("looking up trackers' devices in Home Assistant works again")
		}
	}
	d.cached, d.fetched = m, time.Now()
	return m
}

// Device is one tracker's Home Assistant device ID, or "".
func (d *Devices) Device(ctx context.Context, publicID string) string {
	return d.All(ctx)[publicID]
}

func (d *Devices) fetch(ctx context.Context) (map[string]string, error) {
	body, _ := json.Marshal(map[string]string{"template": deviceTemplate})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.URL, "/")+"/template", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Content-Type", "application/json")
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Home Assistant answered %s: %s", res.Status, strings.TrimSpace(string(out)))
	}
	return parse(string(out)), nil
}

// parse reads the template's output.
func parse(s string) map[string]string {
	m := map[string]string{}
	for _, item := range strings.Split(s, ";") {
		parts := strings.Split(strings.TrimSpace(item), "|")
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		domain, id, device := parts[0], parts[1], parts[2]
		switch {
		case domain == "mqtt" && strings.HasPrefix(id, "ciconia_"):
			m[strings.TrimPrefix(id, "ciconia_")] = device
		case domain == "ciconia" && id != "":
			if _, ok := m[id]; !ok { // MQTT first: it's the relay's own
				m[id] = device
			}
		}
	}
	return m
}
