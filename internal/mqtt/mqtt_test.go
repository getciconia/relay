package mqtt

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/getciconia/relay/codec8"
	"github.com/getciconia/relay/internal/store"
)

// broker starts an MQTT broker for the test and returns its address.
func startBroker(t *testing.T) string {
	t.Helper()
	server := mochi.New(&mochi.Options{})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := server.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return "mqtt://" + tcp.Address()
}

// watcher records the last message on every topic, like a broker's retained
// store seen from outside.
type watcher struct {
	mu   sync.Mutex
	last map[string][]byte
	hits map[string]int
	c    paho.Client
}

func watch(t *testing.T, addr string) *watcher {
	t.Helper()
	w := &watcher{last: map[string][]byte{}, hits: map[string]int{}}
	b, _, _ := broker(addr)
	w.c = paho.NewClient(paho.NewClientOptions().AddBroker(b).SetClientID("watcher"))
	if tok := w.c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("watcher connect", tok.Error())
	}
	w.c.Subscribe("#", 1, func(_ paho.Client, m paho.Message) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.last[m.Topic()] = append([]byte(nil), m.Payload()...)
		w.hits[m.Topic()]++
	}).Wait()
	t.Cleanup(func() { w.c.Disconnect(100) })
	return w
}

// waitFor polls until ok says yes for the topic's last payload.
func (w *watcher) waitFor(t *testing.T, topic string, what string, ok func(payload []byte, seen bool) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		p, seen := w.last[topic]
		w.mu.Unlock()
		if ok(p, seen) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s; last %q (seen %v)", topic, what, p, seen)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (w *watcher) count(topic string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits[topic]
}

func equals(want string) func([]byte, bool) bool {
	return func(p []byte, seen bool) bool { return seen && string(p) == want }
}

type discoveryMsg struct {
	Device struct {
		Identifiers []string `json:"identifiers"`
		Name        string   `json:"name"`
		Model       string   `json:"model"`
		URL         string   `json:"configuration_url"`
	} `json:"device"`
	Availability string                    `json:"availability_topic"`
	Components   map[string]map[string]any `json:"components"`
}

func TestPublishesTrackersForHomeAssistant(t *testing.T) {
	addr := startBroker(t)
	w := watch(t, addr)

	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	car, _ := st.Knock(ctx, "356307042441013")
	st.AddTracker(ctx, car.ID, "Car")
	now := time.Now().UTC().Truncate(time.Second)
	st.SavePositions(ctx, car.ID, []codec8.Record{{Time: now, Latitude: 52.40636, Longitude: 16.92517, Altitude: 80, Satellites: 9, Speed: 0,
		IO: map[uint16]codec8.IOValue{113: codec8.U8(87), 69: codec8.U8(1), 182: codec8.U16(9), 21: codec8.U8(4)}}})

	p := &Publisher{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Interval: time.Hour,
		Config: Config{URL: addr, DeviceURL: "homeassistant://hassio/ingress/local_ciconia_relay", Version: "test"}}
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	id := car.PublicID
	w.waitFor(t, "ciconia/status", "online", equals("online"))
	raw := w.waitFor(t, "homeassistant/device/ciconia_"+id+"/config", "discovery", func(p []byte, seen bool) bool { return len(p) > 0 })
	var d discoveryMsg
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Device.Name != "Car" || d.Device.Identifiers[0] != "ciconia_"+id || d.Device.Model != "Teltonika ATM700" ||
		d.Device.URL != "homeassistant://hassio/ingress/local_ciconia_relay" || d.Availability != "ciconia/status" {
		t.Errorf("device %+v", d)
	}
	loc := d.Components["location"]
	if loc["platform"] != "device_tracker" || loc["json_attributes_topic"] != "ciconia/"+id+"/location" || loc["name"] != nil {
		t.Errorf("location component %v", loc)
	}
	if b := d.Components["battery_level"]; b["device_class"] != "battery" || b["state_topic"] != "ciconia/"+id+"/state" ||
		b["value_template"] != "{{ value_json.battery_level }}" {
		t.Errorf("battery component %v", b)
	}
	if h := d.Components["hdop"]; h["enabled_by_default"] != false || h["entity_category"] != "diagnostic" {
		t.Errorf("hdop component %v", h)
	}
	if len(d.Components) != 12 {
		t.Errorf("%d components", len(d.Components))
	}

	var location map[string]any
	json.Unmarshal(w.waitFor(t, "ciconia/"+id+"/location", "location", func(p []byte, seen bool) bool { return len(p) > 0 }), &location)
	if location["latitude"] != 52.40636 || location["longitude"] != 16.92517 || location["gps_accuracy"] != 5.0 || location["battery_level"] != 87.0 {
		t.Errorf("location %v", location)
	}
	var state map[string]any
	json.Unmarshal(w.waitFor(t, "ciconia/"+id+"/state", "state", func(p []byte, seen bool) bool { return len(p) > 0 }), &state)
	if state["name"] != "Car" || state["status"] != "stopped" || state["satellites"] != 9.0 || state["gps_signal"] != 5.0 ||
		state["gsm_signal"] != 4.0 || state["chip_temperature"] != nil {
		t.Errorf("state %v", state)
	}

	// A new position is published right away.
	st.SavePositions(ctx, car.ID, []codec8.Record{{Time: now.Add(time.Second), Latitude: 52.5, Longitude: 16.9, Speed: 40,
		IO: map[uint16]codec8.IOValue{69: codec8.U8(1)}}})
	w.waitFor(t, "ciconia/"+id+"/state", "moving", func(p []byte, _ bool) bool {
		json.Unmarshal(p, &state)
		return state["status"] == "moving" && state["latitude"] == 52.5
	})

	// Renaming changes the device's name.
	st.RenameTracker(ctx, car.ID, "Family car")
	w.waitFor(t, "homeassistant/device/ciconia_"+id+"/config", "renamed", func(p []byte, _ bool) bool {
		json.Unmarshal(p, &d)
		return d.Device.Name == "Family car"
	})

	// Home Assistant restarting: discovery again.
	before := w.count("homeassistant/device/ciconia_" + id + "/config")
	w.c.Publish("homeassistant/status", 0, false, "online").Wait()
	deadline := time.Now().Add(5 * time.Second)
	for w.count("homeassistant/device/ciconia_"+id+"/config") == before {
		if time.Now().After(deadline) {
			t.Fatal("discovery not sent again after Home Assistant came online")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Removing the tracker removes the device and clears its topics.
	st.DeleteTracker(ctx, car.ID)
	empty := func(p []byte, seen bool) bool { return seen && len(p) == 0 }
	w.waitFor(t, "homeassistant/device/ciconia_"+id+"/config", "removed", empty)
	w.waitFor(t, "ciconia/"+id+"/state", "cleared", empty)
	w.waitFor(t, "ciconia/"+id+"/location", "cleared", empty)

	// Stopping says offline.
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "ciconia/status", "offline", equals("offline"))
	if st := p.Status(); st.Connected || st.Broker != addr {
		t.Errorf("status after stop %+v", st)
	}
}

func TestRemovesTrackersDeletedWhileStopped(t *testing.T) {
	addr := startBroker(t)
	w := watch(t, addr)
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	bike, _ := st.Knock(ctx, "356307042441014")
	st.AddTracker(ctx, bike.ID, "Bike")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	topic := "homeassistant/device/ciconia_" + bike.PublicID + "/config"

	run := func() (context.CancelFunc, chan error) {
		c, cancel := context.WithCancel(ctx)
		p := &Publisher{Store: st, Log: log, Interval: time.Hour, Config: Config{URL: addr}}
		done := make(chan error, 1)
		go func() { done <- p.Run(c) }()
		return cancel, done
	}
	cancel, done := run()
	w.waitFor(t, topic, "published", func(p []byte, _ bool) bool { return len(p) > 0 })
	cancel()
	<-done

	st.DeleteTracker(ctx, bike.ID)
	cancel, done = run()
	defer func() { cancel(); <-done }()
	w.waitFor(t, topic, "removed on the next start", func(p []byte, seen bool) bool { return seen && len(p) == 0 })
}

func TestBrokerAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"mqtt://core-mosquitto":         "tcp://core-mosquitto:1883",
		"mqtt://u:p@10.0.0.2:1884":      "tcp://10.0.0.2:1884",
		"mqtts://broker.example":        "ssl://broker.example:8883",
		"tcp://localhost:1883":          "tcp://localhost:1883",
		"wss://broker.example:443/mqtt": "wss://broker.example:443/mqtt",
	} {
		got, _, err := broker(in)
		if err != nil || got != want {
			t.Errorf("broker(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"core-mosquitto", "http://x", ""} {
		if _, _, err := broker(bad); err == nil {
			t.Errorf("broker(%q) accepted", bad)
		}
	}
	if got := redact("mqtt://user:secret@host:1883"); got != "mqtt://host:1883" {
		t.Errorf("redact: %q", got)
	}
}
