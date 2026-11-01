# Home Assistant

There are two ways to see your trackers in Home Assistant. Use one, not both,
or every tracker shows up twice.

- **MQTT discovery**: nothing to install in Home Assistant. Needs an MQTT
  broker that Home Assistant uses.
- **The Ciconia integration**: the same integration that reads Ciconia Cloud.

## MQTT discovery

As a Home Assistant app, the relay uses the Mosquitto broker app when it's
installed; nothing to set. With Docker, give it the broker Home Assistant
uses:

```bash
RELAY_MQTT_URL=mqtt://192.168.1.20:1883
RELAY_MQTT_USERNAME=relay
RELAY_MQTT_PASSWORD=…
```

Each tracker becomes a device with a location (`device_tracker`, so zones
work) and the same sensors as the Ciconia integration. Renaming or removing a
tracker in the panel renames or removes the device. When the relay stops, its
trackers show as unavailable. Needs Home Assistant 2024.11 or newer.

### Topics

For Node-RED, openHAB or any other MQTT client. All retained.

| Topic | What |
| --- | --- |
| `ciconia/status` | `online` or `offline` |
| `ciconia/<device_id>/state` | The tracker as JSON, plus `status` (`moving`, `stopped`, `offline`, `no_fix`) and `area` |
| `ciconia/<device_id>/location` | `latitude`, `longitude`, `gps_accuracy`, `battery_level`, `altitude`, `speed`, `fixed_time` |
| `homeassistant/device/ciconia_<device_id>/config` | Home Assistant discovery |

`RELAY_MQTT_TOPIC` and `RELAY_MQTT_DISCOVERY_PREFIX` change the first part.

## The Ciconia integration

1. Install the [Ciconia integration](https://github.com/getciconia/ha-integration)
   from HACS and restart Home Assistant.
2. **Settings → Devices & services → Add integration → Ciconia**. Open
   **Advanced** and set the server address to the relay's panel, e.g.
   `http://192.168.1.20:8080`.
3. Click **Submit**. The panel shows the request with its code: check that it
   matches the one in Home Assistant and click **Allow**.

Connected Home Assistants are listed in the panel under **Integrations**, each
with a **Disconnect** button.

If your browser reaches the relay at a different address than Home Assistant
does, set `RELAY_PANEL_URL` so the link Home Assistant shows opens the panel.
