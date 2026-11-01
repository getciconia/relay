# Ciconia Private Relay

Your own server for Ciconia trackers. Trackers send their positions to a
machine you own, and Home Assistant and Loxone read them from there.
Positions go to your server and nowhere else.

- Receives **Codec 8 / Codec 8 Extended** over TCP (`7171`) or TLS (`7172`).
- **[Home Assistant](docs/home-assistant.md)**: through MQTT discovery, or
  with the Ciconia integration.
- **[Loxone](docs/loxone.md)**: a Virtual HTTP Input template and its values.
- A small web panel: your trackers and their state, areas on a map, setup for
  Home Assistant and Loxone.
- One SQLite file. Positions are kept for 7 days; each tracker's latest
  position is always kept.

## Private Relay or Ciconia Cloud?

[Ciconia Cloud](https://getciconia.com) is the same thing, run by us. Home
Assistant and Loxone work the same with either.

|  | Ciconia Cloud | Private Relay |
| --- | --- | --- |
| Who runs the server | We do | You do |
| Where positions are stored | Ciconia Cloud | Only on your server |
| Port to open in your router | None | The tracker port (7171 or 7172) |
| Ciconia app, trip history, alerts, accounts | Yes | No |

Trackers live on the mobile network, so they must reach the tracker port from
the internet: forward it in your router, or run the relay on a server with a
public address.

## Install as a Home Assistant app

1. In Home Assistant, open the app store, click **⋮ → Repositories** and add
   `https://github.com/getciconia/relay`.
2. Find **Ciconia Private Relay** and click **Install**. It's pre-built for
   `amd64` and `aarch64`, Raspberry Pi included.
3. Start it and open **Ciconia Private Relay** in the sidebar.

With the Mosquitto broker app installed, your trackers show up in Home
Assistant by themselves. The app's options are described in
[configuration](docs/configuration.md#home-assistant-app).

## Or run it with Docker

```bash
git clone https://github.com/getciconia/relay.git
cd relay
docker compose up -d
```

Then open `http://<your server>:8080`.

| Port | What | Reachable from |
| --- | --- | --- |
| `7171` | Trackers, Codec 8 over TCP | **The internet** |
| `7172` | Trackers, Codec 8 over TLS | **The internet** |
| `8080` | Panel, Home Assistant, Loxone | **Your home network only.** The panel has no password. |

No tracker at hand? `docker compose --profile demo up -d` also starts three
simulated trackers. Add them in the panel and watch them move.

More in [configuration](docs/configuration.md): other ports, TLS
certificates, limits and every setting.

## Add a tracker

1. Point the tracker at your server's public address, port `7171` (TCP) or
   `7172` (TLS).
2. When it connects, it appears in the panel under **Waiting to be added**.
   Until you add it, the relay refuses it and stores nothing.
3. Give it a name and click **Add**. On its next connection it sends its
   positions, including the ones it kept while waiting.

## More

- [Home Assistant](docs/home-assistant.md)
- [Loxone](docs/loxone.md)
- [Configuration](docs/configuration.md)
- [Development](docs/development.md)

## License

[MIT](LICENSE)
