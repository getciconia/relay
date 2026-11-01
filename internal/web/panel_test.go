package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getciconia/relay/internal/api"
	"github.com/getciconia/relay/internal/codec8"
	"github.com/getciconia/relay/internal/mqtt"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tlscert"
)

var ctx = context.Background()

type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func setup(t *testing.T) *env {
	t.Helper()
	return setupWith(t, func(*Panel) {})
}

func setupWith(t *testing.T, configure func(*Panel)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	(&api.API{Store: st, Log: log}).Register(mux)
	panel := &Panel{Store: st, Log: log, TrackerPort: "5027", Version: "test"}
	configure(panel)
	if err := panel.Register(mux); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(SameOrigin(mux))
	t.Cleanup(func() { srv.Close(); st.Close() })
	return &env{t, st, srv}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *env) get(path string) (int, string) {
	e.t.Helper()
	res, err := http.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// post submits a form like the browser does; it returns the status and
// where it redirects to.
func (e *env) post(path string, form url.Values, headers ...string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.srv.URL)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode, res.Header.Get("Location")
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("page lacks %q", w)
		}
	}
}

func TestTrackersFlow(t *testing.T) {
	e := setup(t)
	_, body := e.get("/")
	contains(t, body, "No trackers yet", "port <b class=\"mono\">5027</b>")

	waiting, _ := e.st.Knock(ctx, "356307042441013")
	_, body = e.get("/")
	contains(t, body, "Waiting to be added", "356307042441013")

	status, loc := e.post("/trackers/"+itoa(waiting.ID)+"/add", url.Values{"name": {"  Dad's   car "}})
	if status != 303 || loc != "/?n=added" {
		t.Fatalf("add: %d %s", status, loc)
	}
	e.st.SavePositions(ctx, waiting.ID, []codec8.Record{{Time: time.Now().Add(-time.Minute), Latitude: 52.23, Longitude: 21.01, Speed: 0, Satellites: 9,
		IO: map[uint16]codec8.IOValue{113: codec8.U8(77), 21: codec8.U8(3), 69: codec8.U8(1), 182: codec8.U16(9)}}})
	_, body = e.get("/?n=added")
	contains(t, body, "Tracker added", "Dad&#39;s car", "Not moving", "77 %", "data-pins")
	if strings.Contains(body, "Waiting to be added") {
		t.Error("added tracker still waiting")
	}

	_, body = e.get("/trackers/" + itoa(waiting.ID))
	contains(t, body, "356307042441013", "What Loxone gets", "t1_battery", "Teltonika ATM700", "77 %")

	e.post("/trackers/"+itoa(waiting.ID), url.Values{"name": {"Car"}})
	if tr, _ := e.st.TrackerByID(ctx, waiting.ID); tr.Name != "Car" {
		t.Errorf("rename: %q", tr.Name)
	}
	e.post("/trackers/"+itoa(waiting.ID)+"/remove", nil)
	if _, err := e.st.TrackerByID(ctx, waiting.ID); err != store.ErrNotFound {
		t.Errorf("remove: %v", err)
	}

	other, _ := e.st.Knock(ctx, "1")
	e.post("/trackers/"+itoa(other.ID)+"/dismiss", nil)
	if w, _ := e.st.WaitingTrackers(ctx); len(w) != 0 {
		t.Errorf("dismiss left %v", w)
	}
	if status, _ := e.get("/trackers/999"); status != 404 {
		t.Errorf("unknown tracker: %d", status)
	}
}

func TestAreas(t *testing.T) {
	e := setup(t)
	_, body := e.get("/areas")
	contains(t, body, "No areas yet")
	_, body = e.get("/areas/new")
	contains(t, body, "Click the map to place the area", `value="#18bcf2" checked`)

	status, _ := e.post("/areas", url.Values{"name": {"Home"}, "radius": {"150"}, "color": {"#4caf50"}})
	if status != 422 {
		t.Errorf("area without a place: %d", status)
	}
	status, loc := e.post("/areas", url.Values{"name": {"Home"}, "latitude": {"52.23"}, "longitude": {"21.01"}, "radius": {"150"}, "color": {"#4caf50"}})
	if status != 303 || loc != "/areas?n=area_saved" {
		t.Fatalf("create: %d %s", status, loc)
	}
	areas, _ := e.st.Areas(ctx)
	if len(areas) != 1 || areas[0].Color != "#4caf50" || areas[0].Radius != 150 {
		t.Fatalf("areas %+v", areas)
	}
	if status, _ := e.post("/areas/"+itoa(areas[0].ID), url.Values{"name": {"Home"}, "latitude": {"52.23"}, "longitude": {"21.01"}, "radius": {"10"}}); status != 422 {
		t.Errorf("tiny radius accepted: %d", status)
	}
	e.post("/areas/"+itoa(areas[0].ID), url.Values{"name": {"Home sweet home"}, "latitude": {"52.23"}, "longitude": {"21.01"}, "radius": {"300"}, "color": {"bogus"}})
	a, _ := e.st.Area(ctx, areas[0].ID)
	if a.Name != "Home sweet home" || a.Radius != 300 || a.Color != store.AreaColors[0] {
		t.Errorf("update %+v", a)
	}
	_, body = e.get("/areas")
	contains(t, body, "Home sweet home", "300 m radius")
	e.post("/areas/"+itoa(a.ID)+"/delete", nil)
	if all, _ := e.st.Areas(ctx); len(all) != 0 {
		t.Errorf("delete left %v", all)
	}
}

func TestLoxone(t *testing.T) {
	e := setup(t)
	tr, _ := e.st.Knock(ctx, "1")
	e.st.AddTracker(ctx, tr.ID, "Car")
	e.st.SavePositions(ctx, tr.ID, []codec8.Record{{Time: time.Now(), Latitude: 52.23, Longitude: 21.01, Satellites: 9, Speed: 30, IO: map[uint16]codec8.IOValue{}}})

	_, body := e.get("/integrations")
	contains(t, body, "Download template", "t1</b>Car", "/loxone/feed/cil_")
	if strings.Contains(body, "Template out of date") {
		t.Error("out of date before any download")
	}

	res, err := http.Get(e.srv.URL + "/loxone/template")
	if err != nil {
		t.Fatal(err)
	}
	xml, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Disposition"), "VI_Ciconia.xml") || !strings.Contains(string(xml), `Address="`+e.srv.URL+"/loxone/feed/cil_") {
		t.Fatalf("template: %s\n%s", res.Header, xml)
	}
	token := strings.SplitN(strings.SplitN(string(xml), "/loxone/feed/", 2)[1], `"`, 2)[0]

	status, feed := e.get("/loxone/feed/" + token)
	if status != 200 || !strings.Contains(feed, "t1_zone=0\n") || !strings.Contains(feed, "t1_moving=1\n") {
		t.Fatalf("feed %d %q", status, feed)
	}
	if status, _ := e.get("/loxone/feed/cil_wrong"); status != 401 {
		t.Errorf("wrong token: %d", status)
	}

	// Adding an area makes the downloaded template out of date.
	_, loc := e.post("/areas", url.Values{"name": {"Home"}, "latitude": {"52.23"}, "longitude": {"21.01"}, "radius": {"100"}, "color": {"#4caf50"}})
	if loc != "/areas?n=loxone" {
		t.Errorf("no Loxone reminder: %s", loc)
	}
	_, body = e.get("/integrations")
	contains(t, body, "Template out of date", "Read ")
	_, feed = e.get("/loxone/feed/" + token)
	if !strings.Contains(feed, "t1_zone=1\n") || !strings.Contains(feed, "t1_z1_in=1\n") {
		t.Errorf("feed after the area: %q", feed)
	}

	e.post("/loxone/regenerate", nil)
	if status, _ := e.get("/loxone/feed/" + token); status != 401 {
		t.Errorf("old address still works: %d", status)
	}
}

func TestLinkPage(t *testing.T) {
	e := setup(t)
	req, deviceCode, _ := e.st.IssueLink(ctx, "Home Assistant")

	_, body := e.get("/link")
	contains(t, body, "Enter the code from Home Assistant")
	_, body = e.get("/link?code=" + strings.ToLower(req.UserCode))
	contains(t, body, "Allow Home Assistant to see your trackers?", req.DisplayCode())
	_, body = e.get("/link?code=AAAA-AAAA")
	contains(t, body, "That code has expired")

	status, _ := e.post("/link", url.Values{"code": {req.DisplayCode()}, "decision": {"allow"}})
	if status != 200 {
		t.Fatalf("allow: %d", status)
	}
	res, _ := e.st.PollLink(ctx, deviceCode)
	if res.Status != "approved" {
		t.Fatalf("poll after allow: %+v", res)
	}
	_, body = e.get("/integrations")
	contains(t, body, "Home Assistant</b>", "Disconnect")
	conns, _ := e.st.Connections(ctx)
	e.post("/connections/"+itoa(conns[0].ID)+"/disconnect", nil)
	if ok, _ := e.st.Authenticate(ctx, res.Token); ok {
		t.Error("token works after disconnecting")
	}
}

func TestCrossSitePostsRefused(t *testing.T) {
	e := setup(t)
	tr, _ := e.st.Knock(ctx, "1")
	if status, _ := e.post("/trackers/"+itoa(tr.ID)+"/add", url.Values{"name": {"x"}}, "Origin", "https://evil.example"); status != 403 {
		t.Errorf("foreign origin: %d", status)
	}
	if status, _ := e.post("/trackers/"+itoa(tr.ID)+"/add", url.Values{"name": {"x"}}, "Sec-Fetch-Site", "cross-site"); status != 403 {
		t.Errorf("cross-site fetch: %d", status)
	}
	if w, _ := e.st.WaitingTrackers(ctx); len(w) != 1 {
		t.Error("tracker added from another site")
	}
	// Through Home Assistant's ingress the browser's origin is Home Assistant.
	if status, _ := e.post("/trackers/"+itoa(tr.ID)+"/add", url.Values{"name": {"x"}},
		"Origin", "http://homeassistant.local:8123", "X-Ingress-Path", ingress, "X-Forwarded-Host", "homeassistant.local:8123"); status != 303 {
		t.Errorf("post through ingress: %d", status)
	}
	other, _ := e.st.Knock(ctx, "100000000000002")
	if status, _ := e.post("/trackers/"+itoa(other.ID)+"/add", url.Values{"name": {"x"}}, "Origin", "https://evil.example", "Sec-Fetch-Site", "same-origin"); status != 303 {
		t.Errorf("Sec-Fetch-Site same-origin wins: %d", status)
	}
	// Home Assistant's API calls carry no Origin and still work.
	res, err := http.Post(e.srv.URL+"/api/v1/link_requests", "application/json", strings.NewReader(`{}`))
	if err != nil || res.StatusCode != 201 {
		t.Errorf("API post: %v %v", res.StatusCode, err)
	}
}

func TestStaticAndHealth(t *testing.T) {
	e := setup(t)
	for _, path := range []string{"/healthz", "/static/panel.css", "/static/panel.js", "/static/favicon.svg", "/static/leaflet/leaflet.js", "/static/leaflet/leaflet.css"} {
		if status, _ := e.get(path); status != 200 {
			t.Errorf("%s: %d", path, status)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (e *env) getWith(path string, headers ...string) (int, string, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

const ingress = "/api/hassio_ingress/AbC123_-x"

func TestThroughHomeAssistantIngress(t *testing.T) {
	e := setup(t)
	_, body, _ := e.getWith("/", "X-Ingress-Path", ingress)
	contains(t, body, `href="`+ingress+`/areas"`, `src="`+ingress+`/static/panel.js"`, `data-base="`+ingress+`"`)
	if strings.Contains(body, `href="/areas"`) {
		t.Error("a link without the ingress prefix")
	}
	_, loc := e.post("/areas", url.Values{"name": {"Home"}, "latitude": {"52"}, "longitude": {"21"}, "radius": {"100"}}, "X-Ingress-Path", ingress)
	if loc != ingress+"/areas?n=area_saved" {
		t.Errorf("redirect %q", loc)
	}
	// Anything else in the header is ignored.
	_, body, _ = e.getWith("/", "X-Ingress-Path", `/evil"><script>`)
	if strings.Contains(body, "evil") {
		t.Error("a bad X-Ingress-Path made it into the page")
	}
}

func TestApprovalInThePanel(t *testing.T) {
	e := setup(t)
	req, deviceCode, _ := e.st.IssueLink(ctx, "Home Assistant")
	_, body := e.get("/areas")
	contains(t, body, "Home Assistant wants to see your trackers.", req.DisplayCode())
	if status, _ := e.post("/link", url.Values{"code": {req.DisplayCode()}, "decision": {"allow"}}); status != 200 {
		t.Fatalf("allow: %d", status)
	}
	if res, _ := e.st.PollLink(ctx, deviceCode); res.Status != "approved" {
		t.Errorf("poll: %+v", res)
	}
	if _, body := e.get("/areas"); strings.Contains(body, "wants to see your trackers") {
		t.Error("banner still there after allowing")
	}
}

func TestTLSCertificateInThePanel(t *testing.T) {
	certs, err := tlscert.Load("", "", t.TempDir(), []string{"relay.example"})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := certs.Info()
	e := setupWith(t, func(p *Panel) { p.TLS, p.TLSPort = certs, "5028" })
	_, body := e.get("/integrations")
	contains(t, body, "port <b class=\"mono\">5028</b>", info.Fingerprint, "Self-signed", "relay.example")
	status, pem := e.get("/tls/certificate.pem")
	if status != 200 || pem != string(info.PEM) {
		t.Errorf("download %d %q", status, pem)
	}
	if status, _ := setup(t).get("/tls/certificate.pem"); status != 404 {
		t.Errorf("without TLS: %d", status)
	}
}

func TestAddonAddresses(t *testing.T) {
	// Port 8080 not published: Loxone can't reach the relay, and says so.
	e := setupWith(t, func(p *Panel) { p.HAURL = "http://local-ciconia-relay:8080" })
	_, body, _ := e.getWith("/integrations", "X-Ingress-Path", ingress)
	contains(t, body, "http://local-ciconia-relay:8080", "publish this app's port <b>8080</b>")
	if status, _, _ := e.getWith("/loxone/template", "X-Ingress-Path", ingress); status != 409 {
		t.Errorf("template without a reachable address: %d", status)
	}

	// Published on 8081: the Miniserver uses the machine Home Assistant runs on.
	e = setupWith(t, func(p *Panel) { p.LoxonePort = "8081" })
	_, xml, _ := e.getWith("/loxone/template", "X-Ingress-Path", ingress, "X-Forwarded-Host", "homeassistant.local:8123")
	if !strings.Contains(xml, `Address="http://homeassistant.local:8081/loxone/feed/cil_`) {
		t.Errorf("template address: %.300s", xml)
	}

	// Set by hand, it wins.
	e = setupWith(t, func(p *Panel) { p.LoxoneURL = "http://192.168.1.5:8080/" })
	_, xml, _ = e.getWith("/loxone/template", "X-Ingress-Path", ingress)
	if !strings.Contains(xml, `Address="http://192.168.1.5:8080/loxone/feed/cil_`) {
		t.Errorf("template address: %.300s", xml)
	}
}

type mqttStatus mqtt.Status

func (s mqttStatus) Status() mqtt.Status { return mqtt.Status(s) }

func TestMQTTInThePanel(t *testing.T) {
	e := setupWith(t, func(p *Panel) {
		p.MQTT = mqttStatus{Broker: "mqtt://core-mosquitto:1883", Topic: "ciconia", Prefix: "homeassistant", Error: "not authorized"}
	})
	_, body := e.get("/integrations")
	contains(t, body, "Through MQTT", "Not connected", "mqtt://core-mosquitto:1883", "not authorized", "Or with the Ciconia integration")
	if _, body := setup(t).get("/integrations"); strings.Contains(body, "Through MQTT") {
		t.Error("MQTT shown while it's off")
	}
}
