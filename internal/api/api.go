// Package api is what Home Assistant talks to: the same endpoints and JSON
// as the Ciconia Cloud API (/api/v1/devices, linking with a code), so the
// Ciconia integration works with the relay by pointing it at its address.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tracker"
)

// API serves /api/v1.
type API struct {
	Store *store.Store
	Log   *slog.Logger
	// PanelURL is how a browser reaches the panel, for the link Home
	// Assistant shows. Empty: the address Home Assistant used.
	PanelURL string
	// ApproveURL, when set, is the exact link Home Assistant shows (as a Home
	// Assistant app: its panel inside Home Assistant).
	ApproveURL string
}

// Register adds the API's routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/devices", a.auth(a.listDevices))
	mux.HandleFunc("GET /api/v1/devices/{public_id}", a.auth(a.showDevice))
	mux.HandleFunc("POST /api/v1/link_requests", a.createLink)
	mux.HandleFunc("POST /api/v1/link_requests/poll", a.pollLink)
}

func (a *API) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ok, err := a.Store.Authenticate(r.Context(), token)
		if err != nil {
			a.fail(w, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

type deviceSummary struct {
	DeviceID string  `json:"device_id"`
	Name     string  `json:"name"`
	Model    *string `json:"model"`
	Status   string  `json:"status"`
}

type device struct {
	DeviceID string  `json:"device_id"`
	Name     string  `json:"name"`
	Model    *string `json:"model"`
	tracker.Position
}

func model(e tracker.Entry) *string {
	if e.Model == "" {
		return nil
	}
	return &e.Model
}

func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	trackers, err := a.Store.ActiveTrackers(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]deviceSummary, 0, len(trackers))
	for _, t := range trackers {
		out = append(out, deviceSummary{DeviceID: t.PublicID, Name: t.Name, Model: model(tracker.Entry{Model: t.Model}), Status: "ready"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (a *API) showDevice(w http.ResponseWriter, r *http.Request) {
	t, err := a.Store.ActiveTrackerByPublicID(r.Context(), r.PathValue("public_id"))
	if errors.Is(err, store.ErrNotFound) {
		// Same answer as a bad token, so ids can't be guessed.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	latest, err := a.Store.LatestPosition(r.Context(), t.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": device{
		DeviceID: t.PublicID, Name: t.Name, Model: model(tracker.Entry{Model: t.Model}),
		Position: tracker.FromStored(latest, a.Store.Now()),
	}})
}

func (a *API) createLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientName string `json:"client_name"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	req, deviceCode, err := a.Store.IssueLink(r.Context(), body.ClientName)
	if err != nil {
		a.fail(w, err)
		return
	}
	link := a.panelURL(r) + "/link"
	complete := link + "?code=" + req.DisplayCode()
	if a.ApproveURL != "" {
		link, complete = a.ApproveURL, a.ApproveURL
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user_code":                 req.DisplayCode(),
		"device_code":               deviceCode,
		"verification_url":          link,
		"verification_url_complete": complete,
		"expires_in":                int(store.LinkLifetime.Seconds()),
		"interval":                  store.LinkPollInterval,
	})
}

func (a *API) pollLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	res, err := a.Store.PollLink(r.Context(), body.DeviceCode)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if res.Status != "approved" {
		writeJSON(w, http.StatusOK, map[string]string{"status": res.Status})
		return
	}
	accountID, err := InstanceID(r, a.Store)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "approved", "api_token": res.Token, "account_id": accountID, "account_email": nil,
	})
}

// InstanceID identifies this relay, the way an account id identifies a
// Ciconia Cloud account: Home Assistant keeps one entry per relay.
func InstanceID(r *http.Request, s *store.Store) (string, error) {
	return s.Setting(r.Context(), "instance_id", func() string { return store.RandomToken("rl_", 8) })
}

func (a *API) panelURL(r *http.Request) string {
	if a.PanelURL != "" {
		return strings.TrimRight(a.PanelURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *API) fail(w http.ResponseWriter, err error) {
	a.Log.Error("api", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
