# Development

Go 1.26.

```bash
go test ./...                 # everything, including the shared Loxone files
go run ./cmd/relay            # relay.db in the current folder, ports 7171, 7172, 8080
go run ./cmd/fake-tracker     # a simulated tracker driving in circles around Warsaw
```

`cmd/fake-tracker` takes `-addr`, `-imei`, `-n` (how many), `-lat`, `-lng`,
`-every`, `-count`, `-8e` (Codec 8 Extended), `-tls` and `-ca` (the
certificate to trust).

## Layout

| Package | What |
| --- | --- |
| `codec8` | Codec 8 / 8E: IMEI greeting, AVL packets, CRC, ACK |
| `internal/ingest` | The tracker ports and their limits |
| `internal/tlscert` | The TLS certificate: your own, or self-signed |
| `internal/tracker` | Records → position fields (IO mapping), tracker state, areas |
| `internal/store` | SQLite: trackers, positions, areas, Home Assistant connections |
| `internal/api` | `/api/v1` for the Home Assistant integration |
| `internal/mqtt` | MQTT with Home Assistant discovery |
| `internal/hass` | Home Assistant's API, for device links in the app |
| `internal/loxone` | The Loxone values and template |
| `internal/web` | The panel, the Loxone feed and the approval page |

`testdata/loxone` holds a scenario and the exact feed and template Ciconia
Cloud makes from it. The relay and Ciconia Cloud both test against these
files, so Loxone gets the same thing from either.

## The Home Assistant app

To try an unreleased version, `ciconia_relay/make-app.sh` puts it together in
`dist/ciconia_relay` without the pre-built image. Copy that folder into Home
Assistant's `local_apps`, click **Check for updates** in the app store, and
install it from the local apps; Home Assistant builds it on the device.

**Releasing:** bump `version` in `ciconia_relay/config.yaml` and add an entry
to `ciconia_relay/CHANGELOG.md`. The push to `master` builds, signs and
publishes it to `ghcr.io/getciconia/ciconia-relay-app`. A published version
is never overwritten.
