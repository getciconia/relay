package web

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getciconia/relay/internal/loxone"
	"github.com/getciconia/relay/internal/mqtt"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tlscert"
)

// baseURL is how the browser reached the panel.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// haURL is the relay's address for the Home Assistant integration.
func (p *Panel) haURL(r *http.Request) string {
	if p.HAURL != "" {
		return p.HAURL
	}
	return baseURL(r)
}

// loxoneURL is the relay's address for the Miniserver; "" when it has none
// (a Home Assistant app whose port 8080 isn't published).
func (p *Panel) loxoneURL(r *http.Request) string {
	switch {
	case p.LoxoneURL != "":
		return strings.TrimRight(p.LoxoneURL, "/")
	case base(r) == "":
		return baseURL(r)
	case p.LoxonePort == "":
		return ""
	}
	// Through Home Assistant's ingress: the same machine Home Assistant runs
	// on, at the port the app publishes.
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "http://" + net.JoinHostPort(host, p.LoxonePort)
}

func (p *Panel) loxoneToken(ctx context.Context) (string, error) {
	return p.Store.Setting(ctx, "loxone_token", func() string { return store.RandomToken("cil_", 24) })
}

func (p *Panel) integrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conns, err := p.Store.Connections(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	entries, areas, err := p.Store.Entries(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	token, err := p.loxoneToken(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	var lastRead *time.Time
	if v, err := p.Store.Setting(ctx, "loxone_last_read", nil); err == nil {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			t := time.UnixMilli(ms)
			lastRead = &t
		}
	}
	_, downloadErr := p.Store.Setting(ctx, "loxone_template_signature", nil)
	var tlsInfo *tlscert.Info
	if p.TLS != nil {
		if info, err := p.TLS.Info(); err == nil {
			tlsInfo = &info
		}
	}
	var mqttStatus *mqtt.Status
	if p.MQTT != nil {
		st := p.MQTT.Status()
		mqttStatus = &st
	}
	p.render(w, r, "integrations", "Integrations", "integrations", map[string]any{
		"MQTT":        mqttStatus,
		"Connections": conns,
		"RelayURL":    p.haURL(r),
		"LoxoneURL":   p.loxoneURL(r),
		"FeedURL":     p.loxoneURL(r) + "/loxone/feed/" + token,
		"TLSPort":     p.TLSPort,
		"TLS":         tlsInfo,
		"Entries":     entries,
		"Areas":       areas,
		"Downloaded":  downloadErr == nil,
		"Outdated":    p.loxoneOutdated(r),
		"LastRead":    lastRead,
		"Port":        p.TrackerPort,
	})
}

func (p *Panel) disconnect(w http.ResponseWriter, r *http.Request) {
	if err := p.Store.DeleteConnection(r.Context(), idOf(r)); err != nil {
		p.fail(w, err)
		return
	}
	redirect(w, r, "/integrations", "disconnected")
}

func (p *Panel) loxoneTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, areas, err := p.Store.Entries(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	token, err := p.loxoneToken(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	address := p.loxoneURL(r)
	if address == "" {
		http.Error(w, "Publish this app's port 8080 first (the app's Configuration → Network), so the Miniserver can reach the relay.", http.StatusConflict)
		return
	}
	feed := loxone.Feed{Entries: entries, Areas: areas}
	if err := p.Store.SetSetting(ctx, "loxone_template_signature", feed.Signature()); err != nil {
		p.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="VI_Ciconia.xml"`)
	w.Write([]byte(feed.Template(address+"/loxone/feed/"+token, p.Store.Now())))
}

func (p *Panel) loxoneRegenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := p.Store.SetSetting(ctx, "loxone_token", store.RandomToken("cil_", 24)); err != nil {
		p.fail(w, err)
		return
	}
	p.Store.SetSetting(ctx, "loxone_template_signature", "")
	redirect(w, r, "/integrations", "regenerated")
}

func (p *Panel) loxoneFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := p.loxoneToken(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	if r.PathValue("token") != token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entries, areas, err := p.Store.Entries(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	now := p.Store.Now()
	if v, err := p.Store.Setting(ctx, "loxone_last_read", nil); err != nil || olderThanMinute(v, now) {
		p.Store.SetSetting(ctx, "loxone_last_read", strconv.FormatInt(now.UnixMilli(), 10))
	}
	body := (loxone.Feed{Entries: entries, Areas: areas}).Values()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(body))
}

func olderThanMinute(ms string, now time.Time) bool {
	v, err := strconv.ParseInt(ms, 10, 64)
	return err != nil || now.Sub(time.UnixMilli(v)) > time.Minute
}

// tlsCertificate downloads the TLS port's certificate, to give a tracker as
// the CA it trusts.
func (p *Panel) tlsCertificate(w http.ResponseWriter, r *http.Request) {
	if p.TLS == nil {
		http.NotFound(w, r)
		return
	}
	info, err := p.TLS.Info()
	if err != nil {
		p.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="ciconia-relay.pem"`)
	w.Write(info.PEM)
}

// ------------------------------------------------------- Home Assistant

func (p *Panel) link(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	data := map[string]any{"Code": code}
	if code != "" {
		req, err := p.Store.PendingLink(r.Context(), code)
		if err == nil {
			data["Request"] = req
		} else {
			data["Expired"] = true
		}
	}
	p.render(w, r, "link", "Connect Home Assistant", "integrations", data)
}

func (p *Panel) decideLink(w http.ResponseWriter, r *http.Request) {
	req, err := p.Store.PendingLink(r.Context(), r.FormValue("code"))
	if err != nil {
		redirect(w, r, "/link?code="+r.FormValue("code"), "")
		return
	}
	allow := r.FormValue("decision") == "allow"
	if err := p.Store.DecideLink(r.Context(), req.ID, allow); err != nil {
		p.fail(w, err)
		return
	}
	p.render(w, r, "linked", "Connect Home Assistant", "integrations", map[string]any{"Allowed": allow, "Client": req.ClientName})
}
