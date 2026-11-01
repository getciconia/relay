# Configuration

## Docker

Ports taken? Pick others when you start it:

```bash
RELAY_TRACKER_PORT=7271 RELAY_TRACKER_TLS_PORT=7272 RELAY_PANEL_PORT=8081 docker compose up -d
```

Without Compose:

```bash
docker build -t ciconia-relay .
docker run -d --name ciconia-relay --restart unless-stopped \
  -p 7171:7171 -p 7172:7172 -p 8080:8080 \
  -v ciconia-relay:/data -e TZ=Europe/Warsaw \
  ciconia-relay
```

The database is `/data/relay.db`. To keep it in a host folder instead of a
volume (`-v /srv/ciconia:/data`), make the folder writable for the
container's user first: `sudo chown 10001 /srv/ciconia`.

## Home Assistant app

| Option | Default | What it does |
| --- | --- | --- |
| `retention_days` | `7` | How long positions are kept. |
| `mqtt` | `true` | Show trackers in Home Assistant through MQTT (the Mosquitto app, or the broker below). |
| `mqtt_server`, `mqtt_username`, `mqtt_password` | | Another MQTT broker, e.g. `mqtt://192.168.1.20:1883`. |
| `tls_hosts` | | Names or IPs for the self-signed TLS certificate, comma-separated. |
| `certfile`, `keyfile` | self-signed | Your own certificate for the TLS port, from `/ssl` (e.g. `fullchain.pem`, `privkey.pem`). |
| `map_tiles` | OpenStreetMap | Tile URL for the panel's maps. |
| `log_packets` | `false` | Log every packet as hex. |

Ports `7171` and `7172` are published by default; `8080` only if you publish
it under the app's **Network** settings (for Loxone).

## TLS

Port `7172` speaks the same Codec 8 inside TLS 1.2 or newer.

- **Self-signed (default).** On first start the relay creates
  `tracker-tls.crt` and `tracker-tls.key` next to the database (RSA 2048,
  valid 10 years). Download the certificate from the panel
  (**Integrations → Trackers**) and give it to the tracker as the CA to trust.
  Set `RELAY_TLS_HOSTS` to the names or IPs trackers use *before* the first
  start.
- **Your own**, e.g. from Let's Encrypt: set `RELAY_TLS_CERT` and
  `RELAY_TLS_KEY`. A renewed certificate is picked up by itself. In Docker,
  mount the files so user `10001` can read them.

`RELAY_TRACKER_TLS_ADDR=off` turns the TLS port off.

## Limits on the tracker ports

The tracker ports face the internet, so the relay limits them:

- `RELAY_MAX_CONNECTIONS` (500) connections at once, `RELAY_MAX_CONNECTIONS_PER_IP` (20) from one address;
- 15 seconds to send the IMEI, which must be 15 digits; a connection silent for 15 minutes is closed;
- `RELAY_MAX_WAITING` (50) trackers waiting to be added: a new one pushes out the one that tried least recently;
- `RELAY_NEW_TRACKERS_PER_IP_HOUR` (10) new IMEIs from one address an hour.

## Settings

All optional, as environment variables.

| Variable | Default | What it does |
| --- | --- | --- |
| `RELAY_TRACKER_ADDR` | `:7171` | Tracker port, TCP. |
| `RELAY_TRACKER_TLS_ADDR` | `:7172` | Tracker port, TLS; `off` turns it off. |
| `RELAY_TLS_CERT`, `RELAY_TLS_KEY` | self-signed | Your own certificate and key (PEM). |
| `RELAY_TLS_HOSTS` | | Names or IPs for the self-signed certificate, comma-separated. |
| `RELAY_HTTP_ADDR` | `:8080` | Panel, Home Assistant and Loxone. |
| `RELAY_DB` | `relay.db` (`/data/relay.db` in Docker) | The SQLite file. |
| `RELAY_RETENTION_DAYS` | `7` | How long positions are kept. A tracker's latest position is always kept. |
| `RELAY_MQTT_URL` | off | `mqtt://host:1883` or `mqtts://host:8883`. Turns MQTT on. |
| `RELAY_MQTT_USERNAME`, `RELAY_MQTT_PASSWORD` | | The broker's user. |
| `RELAY_MQTT_TOPIC` | `ciconia` | Base topic. |
| `RELAY_MQTT_DISCOVERY_PREFIX` | `homeassistant` | Home Assistant's discovery prefix. |
| `RELAY_PANEL_URL` | the address Home Assistant uses | The panel's address in the link Home Assistant shows. |
| `RELAY_DEVICE_URL` | `RELAY_PANEL_URL` | The link on each tracker's device page in Home Assistant. |
| `RELAY_HA_URL`, `RELAY_LOXONE_URL`, `RELAY_APPROVE_URL` | from the request | Addresses shown to Home Assistant and in the panel, when the relay can't tell them. |
| `RELAY_MAP_TILES` | OpenStreetMap | Tile URL for the panel's maps, `{z}/{x}/{y}` style. |
| `RELAY_MAX_CONNECTIONS` … `RELAY_NEW_TRACKERS_PER_IP_HOUR` | see above | Limits on the tracker ports. |
| `RELAY_LOG_PACKETS` | `false` | Log every packet as hex. |
| `RELAY_DEBUG` | `false` | More logging. |
| `TZ` | UTC | Time zone for times in the panel. |
