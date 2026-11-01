// Package mqtt publishes the trackers to an MQTT broker with Home Assistant
// MQTT discovery: each tracker becomes a device in Home Assistant with a
// location and the same sensors as the Ciconia integration, without the
// integration. Other MQTT clients (Node-RED, openHAB, ioBroker) can read the
// same topics.
//
// Topics, with the default names:
//
//	ciconia/status                         online / offline (retained, last will)
//	ciconia/<device_id>/state              the tracker as the API returns it, plus status and area (retained)
//	ciconia/<device_id>/location           latitude, longitude, gps_accuracy… for the device tracker (retained)
//	homeassistant/device/ciconia_<device_id>/config   discovery (retained; emptied when the tracker is removed)
package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tracker"
)

// Config says where to publish.
type Config struct {
	URL             string // mqtt://host:1883, mqtts://host:8883 (tcp://, ssl://, ws://, wss:// work too)
	Username        string
	Password        string
	Topic           string // base topic, default "ciconia"
	DiscoveryPrefix string // default "homeassistant"
	DeviceURL       string // link on each tracker's device page in Home Assistant; empty: none
	Version         string // the relay's version, shown as the discovery origin
}

// Publisher keeps the broker in step with the trackers.
type Publisher struct {
	Store *store.Store
	Log   *slog.Logger
	Config
	// Interval is how often every tracker's state is published again, so
	// "fix age" and the tracker's status stay current. Default one minute.
	Interval time.Duration

	mu        sync.Mutex
	connected bool
	lastErr   string
	count     int

	client    paho.Client
	full      chan struct{}
	published map[string][]byte // device_id → discovery payload last sent ("" = sent before this run)
}

// Status is what the panel shows.
type Status struct {
	Broker    string // the broker address, without a password
	Topic     string
	Prefix    string
	Connected bool
	Error     string
	Trackers  int
}

// Status reports the connection.
func (p *Publisher) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Broker: redact(p.URL), Topic: p.topic(), Prefix: p.prefix(), Connected: p.connected, Error: p.lastErr, Trackers: p.count}
}

func (p *Publisher) topic() string  { return strings.Trim(or(p.Topic, "ciconia"), "/") }
func (p *Publisher) prefix() string { return strings.Trim(or(p.DiscoveryPrefix, "homeassistant"), "/") }

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (p *Publisher) availabilityTopic() string { return p.topic() + "/status" }
func (p *Publisher) stateTopic(id string) string {
	return p.topic() + "/" + id + "/state"
}
func (p *Publisher) locationTopic(id string) string {
	return p.topic() + "/" + id + "/location"
}
func (p *Publisher) discoveryTopic(id string) string {
	return p.prefix() + "/device/ciconia_" + id + "/config"
}

// broker turns mqtt:// and mqtts:// into the schemes the client knows.
func broker(raw string) (string, *url.Userinfo, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("MQTT address %q: want mqtt://host:1883", raw)
	}
	user := u.User
	u.User = nil
	switch u.Scheme {
	case "mqtt", "tcp":
		u.Scheme = "tcp"
		if u.Port() == "" {
			u.Host += ":1883"
		}
	case "mqtts", "ssl", "tls":
		u.Scheme = "ssl"
		if u.Port() == "" {
			u.Host += ":8883"
		}
	case "ws", "wss":
	default:
		return "", nil, fmt.Errorf("MQTT address %q: unknown scheme %q", raw, u.Scheme)
	}
	return u.String(), user, nil
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

// Run connects and publishes until ctx ends, then says offline.
func (p *Publisher) Run(ctx context.Context) error {
	addr, user, err := broker(p.URL)
	if err != nil {
		return err
	}
	if p.Interval == 0 {
		p.Interval = time.Minute
	}
	p.full = make(chan struct{}, 1)
	if err := p.loadPublished(ctx); err != nil {
		return err
	}
	instance, err := p.Store.Setting(ctx, "instance_id", func() string { return store.RandomToken("rl_", 8) })
	if err != nil {
		return err
	}

	opts := paho.NewClientOptions().
		AddBroker(addr).
		SetClientID("ciconia-relay-"+strings.TrimPrefix(instance, "rl_")).
		SetCleanSession(true).
		SetKeepAlive(30*time.Second).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10*time.Second).
		SetMaxReconnectInterval(time.Minute).
		SetWill(p.availabilityTopic(), "offline", 1, true).
		SetOnConnectHandler(p.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			p.setConnected(false, err)
			p.Log.Warn("MQTT connection lost", "err", err)
		})
	if user != nil {
		opts.SetUsername(user.Username())
		if pw, ok := user.Password(); ok {
			opts.SetPassword(pw)
		}
	}
	if p.Username != "" {
		opts.SetUsername(p.Username)
	}
	if p.Password != "" {
		opts.SetPassword(p.Password)
	}
	p.client = paho.NewClient(opts)
	p.Log.Info("MQTT connecting", "broker", redact(p.URL), "topic", p.topic(), "discovery", p.prefix())
	connecting := p.client.Connect() // keeps retrying in the background
	go func() {
		if connecting.Wait() && connecting.Error() != nil {
			p.setConnected(false, connecting.Error())
		}
	}()

	tick := time.NewTicker(p.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.stop()
			return nil
		case <-p.Store.Changes():
			p.sync(ctx, false)
		case <-p.full:
			p.sync(ctx, true)
		case <-tick.C:
			p.sync(ctx, false)
		}
	}
}

func (p *Publisher) stop() {
	if p.client.IsConnectionOpen() {
		p.wait(p.client.Publish(p.availabilityTopic(), 1, true, "offline"))
	}
	p.client.Disconnect(500)
	p.setConnected(false, nil)
}

func (p *Publisher) setConnected(ok bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = ok
	if err != nil {
		p.lastErr = err.Error()
	} else if ok {
		p.lastErr = ""
	}
}

func (p *Publisher) onConnect(c paho.Client) {
	p.setConnected(true, nil)
	p.Log.Info("MQTT connected", "broker", redact(p.URL))
	p.wait(c.Publish(p.availabilityTopic(), 1, true, "online"))
	// Home Assistant says "online" here when it (re)starts: publish
	// discovery again, a moment later so not every device does it at once.
	c.Subscribe(p.prefix()+"/status", 0, func(_ paho.Client, m paho.Message) {
		if string(m.Payload()) == "online" {
			time.AfterFunc(time.Duration(rand.IntN(3000))*time.Millisecond, p.republish)
		}
	})
	p.republish()
}

func (p *Publisher) republish() {
	select {
	case p.full <- struct{}{}:
	default:
	}
}

func (p *Publisher) wait(t paho.Token) error {
	if !t.WaitTimeout(10 * time.Second) {
		return errors.New("MQTT: no answer from the broker")
	}
	return t.Error()
}

func (p *Publisher) publish(topic string, payload []byte) error {
	return p.wait(p.client.Publish(topic, 1, true, payload))
}

// sync publishes every tracker, removes the ones that are gone, and with
// full also publishes discovery again even if it hasn't changed.
func (p *Publisher) sync(ctx context.Context, full bool) {
	if !p.client.IsConnectionOpen() {
		return
	}
	entries, _, err := p.Store.Entries(ctx)
	if err != nil {
		p.Log.Error("MQTT: reading trackers", "err", err)
		return
	}
	if err := p.publishAll(ctx, entries, full); err != nil {
		p.setConnected(p.client.IsConnectionOpen(), err)
		p.Log.Warn("MQTT publish", "err", err)
	}
}

func (p *Publisher) publishAll(ctx context.Context, entries []tracker.Entry, full bool) error {
	seen := map[string]bool{}
	changed := false
	for _, e := range entries {
		seen[e.PublicID] = true
		disc, err := json.Marshal(p.discovery(e))
		if err != nil {
			return err
		}
		if prev, ok := p.published[e.PublicID]; full || !ok || !bytes.Equal(prev, disc) {
			if err := p.publish(p.discoveryTopic(e.PublicID), disc); err != nil {
				return err
			}
			if !ok {
				changed = true
				p.Log.Info("MQTT: tracker published", "tracker", e.Name, "device_id", e.PublicID)
			}
			p.published[e.PublicID] = disc
		}
		state, _ := json.Marshal(stateOf(e))
		location, _ := json.Marshal(locationOf(e.Position))
		if err := p.publish(p.stateTopic(e.PublicID), state); err != nil {
			return err
		}
		if err := p.publish(p.locationTopic(e.PublicID), location); err != nil {
			return err
		}
	}
	for id := range p.published {
		if seen[id] {
			continue
		}
		// An empty retained message removes the device from Home Assistant
		// and clears the retained state.
		for _, topic := range []string{p.discoveryTopic(id), p.stateTopic(id), p.locationTopic(id)} {
			if err := p.publish(topic, nil); err != nil {
				return err
			}
		}
		delete(p.published, id)
		changed = true
		p.Log.Info("MQTT: tracker removed", "device_id", id)
	}
	p.mu.Lock()
	p.count = len(entries)
	p.mu.Unlock()
	if changed {
		return p.savePublished(ctx)
	}
	return nil
}

// The device ids published, kept so trackers removed while the relay was
// stopped are removed from the broker too.
const publishedKey = "mqtt_published"

func (p *Publisher) loadPublished(ctx context.Context) error {
	raw, err := p.Store.Setting(ctx, publishedKey, func() string { return "[]" })
	if err != nil {
		return err
	}
	var ids []string
	json.Unmarshal([]byte(raw), &ids)
	p.published = make(map[string][]byte, len(ids))
	for _, id := range ids {
		p.published[id] = nil
	}
	return nil
}

func (p *Publisher) savePublished(ctx context.Context) error {
	ids := make([]string, 0, len(p.published))
	for id := range p.published {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	raw, _ := json.Marshal(ids)
	return p.Store.SetSetting(ctx, publishedKey, string(raw))
}
