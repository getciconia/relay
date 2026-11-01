// Package web is the relay's private side: the panel (trackers, areas,
// Home Assistant and Loxone), the Loxone feed and the Home Assistant
// approval page. It has no accounts, so its port must stay on the home
// network; only the tracker port is meant to be public.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getciconia/relay/internal/loxone"
	"github.com/getciconia/relay/internal/mqtt"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tlscert"
	"github.com/getciconia/relay/internal/tracker"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Panel serves the web panel and the Loxone feed.
type Panel struct {
	Store       *store.Store
	Log         *slog.Logger
	TrackerPort string          // shown in the panel: where trackers connect
	TLSPort     string          // the same over TLS; empty when off
	TLS         *tlscert.Source // the TLS port's certificate
	Version     string
	MapTiles    string                            // tile URL template; empty = OpenStreetMap
	HAURL       string                            // the address Home Assistant uses, when the request can't tell (Home Assistant app)
	LoxoneURL   string                            // the address the Miniserver uses, likewise
	LoxonePort  string                            // as a Home Assistant app: the host port 8080 is published on ("" = not published)
	MQTT        interface{ Status() mqtt.Status } // nil: MQTT is off
	// HAApp: running as a Home Assistant app. The panel then leaves out
	// Loxone (the feed and template URLs still work) and, with HADevices,
	// links trackers to their devices in Home Assistant.
	HAApp     bool
	HADevices interface {
		All(ctx context.Context) map[string]string // public ID → Home Assistant device ID
	}

	pages map[string]*template.Template
}

// Register adds the panel's routes to mux.
func (p *Panel) Register(mux *http.ServeMux) error {
	if err := p.parse(); err != nil {
		return err
	}
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("GET /{$}", p.trackers)
	mux.HandleFunc("POST /trackers/{id}/add", p.addTracker)
	mux.HandleFunc("POST /trackers/{id}/dismiss", p.dismissTracker)
	mux.HandleFunc("GET /trackers/{id}", p.showTracker)
	mux.HandleFunc("POST /trackers/{id}", p.renameTracker)
	mux.HandleFunc("POST /trackers/{id}/remove", p.removeTracker)

	mux.HandleFunc("GET /areas", p.areas)
	mux.HandleFunc("GET /areas/new", p.newArea)
	mux.HandleFunc("POST /areas", p.createArea)
	mux.HandleFunc("GET /areas/{id}", p.editArea)
	mux.HandleFunc("POST /areas/{id}", p.updateArea)
	mux.HandleFunc("POST /areas/{id}/delete", p.deleteArea)

	mux.HandleFunc("GET /integrations", p.integrations)
	mux.HandleFunc("POST /connections/{id}/disconnect", p.disconnect)
	mux.HandleFunc("GET /loxone/template", p.loxoneTemplate)
	mux.HandleFunc("POST /loxone/regenerate", p.loxoneRegenerate)
	mux.HandleFunc("GET /loxone/feed/{token}", p.loxoneFeed)

	mux.HandleFunc("GET /tls/certificate.pem", p.tlsCertificate)

	mux.HandleFunc("GET /link", p.link)
	mux.HandleFunc("POST /link", p.decideLink)
	return nil
}

// SameOrigin refuses form posts from other sites: without accounts, any web
// page the user visits could otherwise post to the panel from their browser.
// Browsers say where a request comes from in Sec-Fetch-Site; older ones only
// send Origin, which must then match the address the browser used (through
// Home Assistant's ingress: Home Assistant's own address).
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasPrefix(r.URL.Path, "/api/") && !sameOrigin(r) {
			http.Error(w, "Cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "same-site", "none":
		return true
	case "cross-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser form post
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if base(r) != "" && r.Header.Get("X-Forwarded-Host") != "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	return u.Host == host
}

var funcs = template.FuncMap{
	"json": func(v any) (template.JS, error) {
		b, err := json.Marshal(v)
		return template.JS(b), err
	},
	"ago":  func(t time.Time) string { return tracker.Ago(int64(time.Since(t).Seconds())) },
	"date": func(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") },
	// stamp shows an API time ("2026-10-31T22:10:41Z") in the server's zone.
	"stamp": func(s *string) string {
		if s == nil {
			return "-"
		}
		t, err := time.Parse(time.RFC3339, *s)
		if err != nil {
			return *s
		}
		return t.Local().Format("2 Jan 2006, 15:04:05")
	},
	"num": func(f *float64, unit string) string {
		if f == nil {
			return "-"
		}
		return strconv.FormatFloat(*f, 'f', -1, 64) + unit
	},
	"int": func(n *int64, unit string) string {
		if n == nil {
			return "-"
		}
		return strconv.FormatInt(*n, 10) + unit
	},
	"str": func(s *string) string {
		if s == nil {
			return "-"
		}
		return *s
	},
}

func (p *Panel) parse() error {
	p.pages = map[string]*template.Template{}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, page := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(page, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", page)
		if err != nil {
			return fmt.Errorf("%s: %w", page, err)
		}
		p.pages[name] = t
	}
	return nil
}

type page struct {
	Title   string
	Nav     string
	Notice  string
	Version string
	Tiles   string
	Base    string              // path prefix when served through Home Assistant's ingress
	HAApp   bool                // a Home Assistant app: no Loxone in the panel
	Pending []store.LinkRequest // Home Assistants waiting for approval
	Data    any
}

var notices = map[string]string{
	"added":        "Tracker added. Its positions arrive the next time it connects.",
	"dismissed":    "Removed from the list. It shows up again if it connects.",
	"renamed":      "Saved.",
	"removed":      "Tracker removed, with its positions.",
	"area_saved":   "Area saved.",
	"area_deleted": "Area deleted.",
	"disconnected": "Disconnected. That Home Assistant can't read the relay any more.",
	"regenerated":  "New Loxone address made. Download the template again and import it; the old address stopped working.",
	"loxone":       "Your Loxone template is out of date. Download it again and import it.",
}

func (p *Panel) render(w http.ResponseWriter, r *http.Request, name, title, nav string, data any) {
	t, ok := p.pages[name]
	if !ok {
		http.Error(w, "no page "+name, http.StatusInternalServerError)
		return
	}
	pending, err := p.Store.PendingLinks(r.Context())
	if err != nil {
		p.Log.Error("pending links", "err", err)
	}
	pg := page{Title: title, Nav: nav, Notice: notices[r.URL.Query().Get("n")], Version: p.Version, Tiles: p.tiles(),
		Base: base(r), HAApp: p.HAApp, Pending: pending, Data: data}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, pg); err != nil {
		p.Log.Error("render", "page", name, "err", err)
	}
}

// DefaultTiles is OpenStreetMap's own tile server.
const DefaultTiles = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

func (p *Panel) tiles() string {
	if p.MapTiles != "" {
		return p.MapTiles
	}
	return DefaultTiles
}

func (p *Panel) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, nil)
		return
	}
	p.Log.Error("panel", "err", err)
	http.Error(w, "Something went wrong. See the relay's log.", http.StatusInternalServerError)
}

func redirect(w http.ResponseWriter, r *http.Request, path, notice string) {
	if notice != "" {
		path += "?n=" + notice
	}
	http.Redirect(w, r, base(r)+path, http.StatusSeeOther)
}

var ingressPath = regexp.MustCompile(`^/api/hassio_ingress/[A-Za-z0-9_-]+$`)

// haDevices maps trackers' public IDs to their Home Assistant device IDs,
// for links to their device pages. Empty unless the panel is open inside
// Home Assistant (through ingress), where /config/devices/... is Home
// Assistant's own page.
func (p *Panel) haDevices(r *http.Request) map[string]string {
	if p.HADevices == nil || base(r) == "" {
		return nil
	}
	return p.HADevices.All(r.Context())
}

// base is the path prefix the browser sees: Home Assistant's ingress serves
// an app's panel under /api/hassio_ingress/<token>, and says so in
// X-Ingress-Path. Empty otherwise.
func base(r *http.Request) string {
	if p := strings.TrimRight(r.Header.Get("X-Ingress-Path"), "/"); ingressPath.MatchString(p) {
		return p
	}
	return ""
}

func idOf(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// mapTracker and mapArea are what the maps draw.
type mapTracker struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Badge string  `json:"badge"`
	State string  `json:"state"`
}

type mapArea struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Radius int     `json:"radius"`
	Color  string  `json:"color"`
}

func mapAreas(areas []tracker.Area) []mapArea {
	out := make([]mapArea, 0, len(areas))
	for _, a := range areas {
		out = append(out, mapArea{ID: a.ID, Name: a.Name, Lat: a.Latitude, Lng: a.Longitude, Radius: a.Radius, Color: a.Color})
	}
	return out
}

func stateClass(s tracker.Status) string {
	switch {
	case s.Key == tracker.Moving:
		return "moving"
	case s.Key == tracker.Stopped && s.Area != nil:
		return "home"
	case s.Key == tracker.Stopped:
		return "parked"
	}
	return "offline"
}

// loxoneOutdated says whether a downloaded template no longer matches the
// trackers and areas.
func (p *Panel) loxoneOutdated(r *http.Request) bool {
	entries, areas, err := p.Store.Entries(r.Context())
	if err != nil {
		return false
	}
	downloaded, err := p.Store.Setting(r.Context(), "loxone_template_signature", nil)
	if err != nil {
		return false
	}
	return downloaded != (loxone.Feed{Entries: entries, Areas: areas}).Signature()
}

// afterChange redirects, adding the Loxone reminder when the change made a
// downloaded template out of date (not as a Home Assistant app, whose panel
// doesn't show Loxone).
func (p *Panel) afterChange(w http.ResponseWriter, r *http.Request, path, notice string) {
	if !p.HAApp && p.loxoneOutdated(r) {
		notice = "loxone"
	}
	redirect(w, r, path, notice)
}
