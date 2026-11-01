package hass

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDevices(t *testing.T) {
	var calls atomic.Int32
	answer := "mqtt|ciconia_abc123|dev1;\nmqtt|other_thing|dev9;ciconia|xyz789|dev2;ciconia|abc123|dev3;"
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/core/api/template" || r.Header.Get("Authorization") != "Bearer tok" || !strings.Contains(body["template"], "device_attr") {
			t.Errorf("request %s %v %q", r.URL.Path, r.Header, body["template"])
		}
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	defer srv.Close()

	d := &Devices{URL: srv.URL + "/core/api/", Token: "tok", TTL: time.Hour}
	got := d.All(context.Background())
	want := map[string]string{"abc123": "dev1", "xyz789": "dev2"}
	if len(got) != len(want) || got["abc123"] != "dev1" || got["xyz789"] != "dev2" {
		t.Errorf("got %v, want %v", got, want)
	}
	if d.Device(context.Background(), "nope") != "" || calls.Load() != 1 {
		t.Errorf("cache: %d calls", calls.Load())
	}

	status = http.StatusUnauthorized
	d2 := &Devices{URL: srv.URL + "/core/api", Token: "tok"}
	if m := d2.All(context.Background()); len(m) != 0 {
		t.Errorf("an error should give no devices: %v", m)
	}
}
