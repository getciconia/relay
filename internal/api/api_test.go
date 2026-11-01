package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getciconia/relay/codec8"
	"github.com/getciconia/relay/internal/store"
)

var ctx = context.Background()

func setup(t *testing.T) (*store.Store, *httptest.Server, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	st.SetClock(func() time.Time { return now })
	mux := http.NewServeMux()
	(&API{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { srv.Close(); st.Close() })
	return st, srv, &now
}

func call(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// link goes through the whole linking flow and returns the token.
func link(t *testing.T, st *store.Store, base string) (string, string) {
	t.Helper()
	status, created := call(t, "POST", base+"/api/v1/link_requests", "", map[string]string{"client_name": "Home Assistant"})
	if status != 201 {
		t.Fatalf("create link: %d %v", status, created)
	}
	code := created["user_code"].(string)
	if !strings.HasPrefix(created["verification_url"].(string), base+"/link") ||
		created["verification_url_complete"] != base+"/link?code="+code || created["expires_in"] != 600.0 || created["interval"] != 5.0 {
		t.Errorf("link request %v", created)
	}
	device := created["device_code"].(string)
	if _, polled := call(t, "POST", base+"/api/v1/link_requests/poll", "", map[string]string{"device_code": device}); polled["status"] != "pending" {
		t.Errorf("before approval %v", polled)
	}
	req, err := st.PendingLink(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	st.DecideLink(ctx, req.ID, true)
	_, polled := call(t, "POST", base+"/api/v1/link_requests/poll", "", map[string]string{"device_code": device})
	if polled["status"] != "approved" || polled["account_email"] != nil || !strings.HasPrefix(polled["account_id"].(string), "rl_") {
		t.Fatalf("approved poll %v", polled)
	}
	return polled["api_token"].(string), polled["account_id"].(string)
}

func TestLinkingAndDevices(t *testing.T) {
	st, srv, now := setup(t)
	token, accountID := link(t, st, srv.URL)
	if second, again := link(t, st, srv.URL); second == token || again != accountID {
		t.Errorf("second link: same token or a different relay id")
	}

	if status, _ := call(t, "GET", srv.URL+"/api/v1/devices", "", nil); status != 401 {
		t.Errorf("no token: %d", status)
	}
	if status, _ := call(t, "GET", srv.URL+"/api/v1/devices", "cir_wrong", nil); status != 401 {
		t.Errorf("wrong token: %d", status)
	}
	_, list := call(t, "GET", srv.URL+"/api/v1/devices", token, nil)
	if len(list["devices"].([]any)) != 0 {
		t.Errorf("empty relay lists %v", list)
	}

	car, _ := st.Knock(ctx, "356307042441013")
	st.AddTracker(ctx, car.ID, "Car")
	st.Knock(ctx, "999") // waiting: never listed
	_, list = call(t, "GET", srv.URL+"/api/v1/devices", token, nil)
	devices := list["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices %v", devices)
	}
	d := devices[0].(map[string]any)
	if d["device_id"] != car.PublicID || d["name"] != "Car" || d["model"] != "Teltonika ATM700" || d["status"] != "ready" {
		t.Errorf("summary %v", d)
	}

	// No position yet: every field there, null.
	_, shown := call(t, "GET", srv.URL+"/api/v1/devices/"+car.PublicID, token, nil)
	dev := shown["device"].(map[string]any)
	for _, key := range []string{"latitude", "longitude", "altitude", "speed", "battery_level", "gsm_signal", "gsm_operator_code",
		"chip_temperature", "satellites", "hdop", "gps_signal", "fixed_time", "fix_age_seconds", "device_time", "server_time"} {
		if v, ok := dev[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v)", key, v, ok)
		}
	}
	if attrs, ok := dev["attributes"].(map[string]any); !ok || len(attrs) != 0 {
		t.Errorf("attributes %v", dev["attributes"])
	}

	st.SavePositions(ctx, car.ID, []codec8.Record{{
		Time: now.Add(-37 * time.Second), Latitude: 52.229676, Longitude: 21.012229, Altitude: 112, Speed: 0, Satellites: 11,
		IO: map[uint16]codec8.IOValue{113: codec8.U8(87), 21: codec8.U8(4), 241: codec8.U32(26003), 70: codec8.U16(235), 182: codec8.U16(9), 69: codec8.U8(1)},
	}})
	_, shown = call(t, "GET", srv.URL+"/api/v1/devices/"+car.PublicID, token, nil)
	dev = shown["device"].(map[string]any)
	want := map[string]any{"latitude": 52.229676, "battery_level": 87.0, "gsm_signal": 4.0, "gsm_operator_code": 26003.0,
		"chip_temperature": 23.5, "satellites": 11.0, "hdop": 0.9, "gps_signal": 5.0, "fix_age_seconds": 37.0, "fixed_time": "2026-10-31T11:59:23Z"}
	for k, v := range want {
		if dev[k] != v {
			t.Errorf("%s = %v, want %v", k, dev[k], v)
		}
	}

	if status, body := call(t, "GET", srv.URL+"/api/v1/devices/dv_unknown", token, nil); status != 401 || body["error"] != "unauthorized" {
		t.Errorf("unknown id: %d %v", status, body)
	}
}

func TestPollUnknown(t *testing.T) {
	_, srv, _ := setup(t)
	if status, _ := call(t, "POST", srv.URL+"/api/v1/link_requests/poll", "", map[string]string{"device_code": "nope"}); status != 404 {
		t.Errorf("unknown device code: %d", status)
	}
}

func TestApproveURL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mux := http.NewServeMux()
	(&API{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ApproveURL: "/hassio/ingress/local_ciconia_relay"}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, created := call(t, "POST", srv.URL+"/api/v1/link_requests", "", map[string]string{})
	if created["verification_url"] != "/hassio/ingress/local_ciconia_relay" || created["verification_url_complete"] != "/hassio/ingress/local_ciconia_relay" {
		t.Errorf("link %v", created)
	}
}
