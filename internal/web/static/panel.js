// Ciconia Relay panel: maps (Leaflet + OpenStreetMap, no key), the area
// editor, copy buttons, confirmations and a gentle auto-refresh.
(function () {
  "use strict";

    const ATTRIBUTION = '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors';
  const parse = (el, name) => { try { return JSON.parse(el.dataset[name] || "[]") || []; } catch { return []; } };

  function baseMap(el) {
    const map = L.map(el, { scrollWheelZoom: false });
    L.tileLayer(document.body.dataset.tiles, { maxZoom: 19, attribution: ATTRIBUTION }).addTo(map);
    map.setView([52.2297, 21.0122], 5);
    return map;
  }

  function drawAreas(map, areas, opts = {}) {
    return areas.map((a) => {
      const circle = L.circle([a.lat, a.lng], { radius: a.radius, color: a.color, weight: 2, fillOpacity: 0.12, ...opts })
        .bindTooltip(a.name).addTo(map);
      if (a.id && opts.interactive !== false) circle.on("click", () => (location.href = document.body.dataset.base + "/areas/" + a.id));
      return circle;
    });
  }

  function showMap(el) {
    const map = baseMap(el);
    const pins = parse(el, "pins");
    const areas = drawAreas(map, parse(el, "areas"));
    const markers = pins.map((p) =>
      L.marker([p.lat, p.lng], { icon: L.divIcon({ className: "pin is-" + p.state }) })
        .bindTooltip(p.name + " · " + p.badge, { permanent: true, direction: "right", offset: [12, 0], className: "pin-label" })
        .on("click", () => (location.href = document.body.dataset.base + "/trackers/" + p.id))
        .addTo(map));
    const layers = [...markers, ...areas];
    if (pins.length === 1 && el.dataset.zoom) {
      map.setView([pins[0].lat, pins[0].lng], Number(el.dataset.zoom));
    } else if (layers.length) {
      map.fitBounds(L.featureGroup(layers).getBounds().pad(0.2), { maxZoom: 16 });
    }
  }

  function areaEditor(root) {
    const el = root.querySelector("[data-area-map]");
    const lat = root.querySelector("[data-lat]");
    const lng = root.querySelector("[data-lng]");
    const radius = root.querySelector("[data-radius]");
    const label = root.querySelector("[data-radius-label]");
    const hint = root.querySelector("[data-area-hint]");
    const color = () => (root.querySelector("[data-color]:checked") || {}).value || "#18bcf2";
    const map = baseMap(el);
    const others = drawAreas(map, parse(root, "others"), { interactive: false, opacity: 0.5 });
    let circle = null;

    function place(latlng) {
      lat.value = latlng.lat.toFixed(7);
      lng.value = latlng.lng.toFixed(7);
      if (hint) hint.hidden = true;
      if (!circle) {
        circle = L.circle(latlng, { radius: Number(radius.value), color: color(), weight: 3, fillOpacity: 0.2 }).addTo(map);
      } else {
        circle.setLatLng(latlng);
      }
    }

    if (lat.value && lng.value) {
      place(L.latLng(Number(lat.value), Number(lng.value)));
      map.fitBounds(circle.getBounds().pad(0.6));
    } else if (others.length) {
      map.fitBounds(L.featureGroup(others).getBounds().pad(0.3));
    } else if (navigator.geolocation) {
      navigator.geolocation.getCurrentPosition((p) => map.setView([p.coords.latitude, p.coords.longitude], 15), () => {});
    }
    map.on("click", (e) => place(e.latlng));
    radius.addEventListener("input", () => {
      label.textContent = radius.value + " m";
      if (circle) circle.setRadius(Number(radius.value));
    });
    root.querySelectorAll("[data-color]").forEach((r) =>
      r.addEventListener("change", () => circle && circle.setStyle({ color: color() })));
  }

  window.Panel = {
    // Reloads the page every few seconds, unless the user is typing.
    autoRefresh(seconds) {
      setInterval(() => {
        const a = document.activeElement;
        if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA")) return;
        location.reload();
      }, seconds * 1000);
    },
  };

  window.addEventListener("load", () => {
    if (window.L) {
      document.querySelectorAll("[data-map]").forEach(showMap);
      document.querySelectorAll("[data-area-editor]").forEach(areaEditor);
    }
    document.querySelectorAll("form[data-confirm]").forEach((f) =>
      f.addEventListener("submit", (e) => { if (!confirm(f.dataset.confirm)) e.preventDefault(); }));
    document.querySelectorAll("[data-copy]").forEach((b) =>
      b.addEventListener("click", async () => {
        try { await navigator.clipboard.writeText(b.dataset.copy); b.textContent = "Copied"; }
        catch { b.textContent = "Select it instead"; }
        setTimeout(() => (b.textContent = "Copy"), 2000);
      }));
  });
})();
