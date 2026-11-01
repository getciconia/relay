package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/getciconia/relay/internal/loxone"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tracker"
)

// ------------------------------------------------------------- trackers

type trackerRow struct {
	tracker.Entry
	Badge    string
	State    string
	Updated  string
	HADevice string // its device in Home Assistant, for a link; "" for none
}

func (p *Panel) trackers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, areas, err := p.Store.Entries(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	waiting, err := p.Store.WaitingTrackers(ctx)
	if err != nil {
		p.fail(w, err)
		return
	}
	rows := make([]trackerRow, 0, len(entries))
	pins := make([]mapTracker, 0, len(entries))
	devices := p.haDevices(r)
	for _, e := range entries {
		row := trackerRow{Entry: e, Badge: e.Status.Badge(), State: stateClass(e.Status), HADevice: devices[e.PublicID]}
		if e.Position.FixAgeSeconds != nil {
			row.Updated = tracker.Ago(*e.Position.FixAgeSeconds)
		}
		rows = append(rows, row)
		if e.Position.Located() {
			pins = append(pins, mapTracker{ID: e.ID, Name: e.Name, Lat: *e.Position.Latitude, Lng: *e.Position.Longitude, Badge: row.Badge, State: row.State})
		}
	}
	p.render(w, r, "trackers", "Trackers", "trackers", map[string]any{
		"Trackers": rows, "Waiting": waiting, "Pins": pins, "Areas": mapAreas(areas), "Port": p.TrackerPort,
	})
}

func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func (p *Panel) addTracker(w http.ResponseWriter, r *http.Request) {
	name := cleanName(r.FormValue("name"))
	if name == "" {
		name = "Tracker"
	}
	if err := p.Store.AddTracker(r.Context(), idOf(r), name); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/", "added")
}

func (p *Panel) dismissTracker(w http.ResponseWriter, r *http.Request) {
	t, err := p.Store.TrackerByID(r.Context(), idOf(r))
	if err == nil && t.Status != store.Waiting {
		err = store.ErrNotFound
	}
	if err == nil {
		err = p.Store.DeleteTracker(r.Context(), t.ID)
	}
	if err != nil {
		p.fail(w, err)
		return
	}
	redirect(w, r, "/", "dismissed")
}

func (p *Panel) entry(ctx context.Context, id int64) (tracker.Entry, []tracker.Area, error) {
	entries, areas, err := p.Store.Entries(ctx)
	if err != nil {
		return tracker.Entry{}, nil, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, areas, nil
		}
	}
	return tracker.Entry{}, nil, store.ErrNotFound
}

func (p *Panel) showTracker(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, areas, err := p.entry(ctx, idOf(r))
	if err != nil {
		p.fail(w, err)
		return
	}
	t, err := p.Store.TrackerByID(ctx, e.ID)
	if err != nil {
		p.fail(w, err)
		return
	}
	count, _ := p.Store.PositionCount(ctx, e.ID)
	var pins []mapTracker
	if e.Position.Located() {
		pins = append(pins, mapTracker{ID: e.ID, Name: e.Name, Lat: *e.Position.Latitude, Lng: *e.Position.Longitude, Badge: e.Status.Badge(), State: stateClass(e.Status)})
	}
	feed := loxone.Feed{Entries: []tracker.Entry{e}, Areas: areas}
	type loxoneRow struct {
		Title, Key, Range, Value string
	}
	var lox []loxoneRow
	for _, in := range feed.TrackerInputs(e) {
		lox = append(lox, loxoneRow{Title: in.Title, Key: in.Key, Range: in.Field.Range, Value: feed.Value(e, in)})
	}
	updated := ""
	if e.Position.FixAgeSeconds != nil {
		updated = tracker.Ago(*e.Position.FixAgeSeconds)
	}
	p.render(w, r, "tracker", e.Name, "trackers", map[string]any{
		"E": e, "T": t, "Badge": e.Status.Badge(), "State": stateClass(e.Status), "Updated": updated, "Count": count,
		"Pins": pins, "Areas": mapAreas(areas), "Loxone": lox, "HADevice": p.haDevices(r)[e.PublicID],
	})
}

func (p *Panel) renameTracker(w http.ResponseWriter, r *http.Request) {
	name := cleanName(r.FormValue("name"))
	if name == "" {
		redirect(w, r, "/trackers/"+r.PathValue("id"), "")
		return
	}
	if err := p.Store.RenameTracker(r.Context(), idOf(r), name); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/trackers/"+r.PathValue("id"), "renamed")
}

func (p *Panel) removeTracker(w http.ResponseWriter, r *http.Request) {
	if err := p.Store.DeleteTracker(r.Context(), idOf(r)); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/", "removed")
}

// ---------------------------------------------------------------- areas

func (p *Panel) areas(w http.ResponseWriter, r *http.Request) {
	_, areas, err := p.Store.Entries(r.Context())
	if err != nil {
		p.fail(w, err)
		return
	}
	p.render(w, r, "areas", "Areas", "areas", map[string]any{"Areas": areas, "Map": mapAreas(areas)})
}

type areaForm struct {
	Area   tracker.Area
	New    bool
	Error  string
	Colors []string
	Others []mapArea
}

func (p *Panel) areaPage(w http.ResponseWriter, r *http.Request, f areaForm) {
	all, _ := p.Store.Areas(r.Context())
	for _, a := range all {
		if a.ID != f.Area.ID {
			f.Others = append(f.Others, mapAreas([]tracker.Area{a})...)
		}
	}
	f.Colors = store.AreaColors
	title := "Edit " + f.Area.Name
	if f.New {
		title = "New area"
	}
	if f.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	p.render(w, r, "area", title, "areas", f)
}

func (p *Panel) newArea(w http.ResponseWriter, r *http.Request) {
	p.areaPage(w, r, areaForm{New: true, Area: tracker.Area{Radius: 150, Color: p.Store.NextAreaColor(r.Context())}})
}

func (p *Panel) editArea(w http.ResponseWriter, r *http.Request) {
	a, err := p.Store.Area(r.Context(), idOf(r))
	if err != nil {
		p.fail(w, err)
		return
	}
	p.areaPage(w, r, areaForm{Area: a})
}

// areaFromForm reads and checks the form; the error is for the user.
func areaFromForm(r *http.Request, id int64) (tracker.Area, string) {
	a := tracker.Area{ID: id, Name: cleanName(r.FormValue("name")), Color: r.FormValue("color")}
	var err1, err2, err3 error
	a.Latitude, err1 = strconv.ParseFloat(r.FormValue("latitude"), 64)
	a.Longitude, err2 = strconv.ParseFloat(r.FormValue("longitude"), 64)
	a.Radius, err3 = strconv.Atoi(r.FormValue("radius"))
	switch {
	case a.Name == "":
		return a, "Give the area a name."
	case err1 != nil || err2 != nil || a.Latitude < -90 || a.Latitude > 90 || a.Longitude < -180 || a.Longitude > 180:
		return a, "Click the map to place the area."
	case err3 != nil || a.Radius < 25 || a.Radius > 5000:
		return a, "The radius must be between 25 and 5000 m."
	}
	ok := false
	for _, c := range store.AreaColors {
		ok = ok || c == a.Color
	}
	if !ok {
		a.Color = store.AreaColors[0]
	}
	return a, ""
}

func (p *Panel) createArea(w http.ResponseWriter, r *http.Request) {
	a, problem := areaFromForm(r, 0)
	if problem != "" {
		p.areaPage(w, r, areaForm{Area: a, New: true, Error: problem})
		return
	}
	if _, err := p.Store.CreateArea(r.Context(), a); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/areas", "area_saved")
}

func (p *Panel) updateArea(w http.ResponseWriter, r *http.Request) {
	a, problem := areaFromForm(r, idOf(r))
	if problem != "" {
		p.areaPage(w, r, areaForm{Area: a, Error: problem})
		return
	}
	if err := p.Store.UpdateArea(r.Context(), a); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/areas", "area_saved")
}

func (p *Panel) deleteArea(w http.ResponseWriter, r *http.Request) {
	if err := p.Store.DeleteArea(r.Context(), idOf(r)); err != nil {
		p.fail(w, err)
		return
	}
	p.afterChange(w, r, "/areas", "area_deleted")
}
