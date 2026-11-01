#!/command/with-contenv bashio
# Starts Ciconia Private Relay with the app's options. Every lookup falls back to a
# default, so the relay always starts.

export RELAY_DB=/data/relay.db
export RELAY_TRACKER_ADDR=:7171
export RELAY_TRACKER_TLS_ADDR=:7172
export RELAY_HTTP_ADDR=:8080
# The panel leaves out Loxone and links trackers to their Home Assistant
# devices (with the Supervisor's token, SUPERVISOR_TOKEN).
export RELAY_HA_APP=true
export RELAY_RETENTION_DAYS="$(bashio::config 'retention_days' 7 || echo 7)"
export RELAY_LOG_PACKETS="$(bashio::config 'log_packets' false || echo false)"

if bashio::config.has_value 'tls_hosts' 2>/dev/null; then
  export RELAY_TLS_HOSTS="$(bashio::config 'tls_hosts')"
fi
if bashio::config.has_value 'certfile' 2>/dev/null && bashio::config.has_value 'keyfile' 2>/dev/null; then
  export RELAY_TLS_CERT="/ssl/$(bashio::config 'certfile')"
  export RELAY_TLS_KEY="/ssl/$(bashio::config 'keyfile')"
fi
if bashio::config.has_value 'map_tiles' 2>/dev/null; then
  export RELAY_MAP_TILES="$(bashio::config 'map_tiles')"
fi

# MQTT: the broker set in the options, else the Mosquitto app (the
# Supervisor hands out its address and a user for this app).
if bashio::config.true 'mqtt' 2>/dev/null; then
  if bashio::config.has_value 'mqtt_server' 2>/dev/null; then
    export RELAY_MQTT_URL="$(bashio::config 'mqtt_server')"
    export RELAY_MQTT_USERNAME="$(bashio::config 'mqtt_username' '' || true)"
    export RELAY_MQTT_PASSWORD="$(bashio::config 'mqtt_password' '' || true)"
  elif bashio::services.available 'mqtt' 2>/dev/null; then
    scheme=mqtt
    if bashio::var.true "$(bashio::services 'mqtt' 'ssl' 2>/dev/null || true)"; then
      scheme=mqtts
    fi
    export RELAY_MQTT_URL="${scheme}://$(bashio::services 'mqtt' 'host'):$(bashio::services 'mqtt' 'port')"
    export RELAY_MQTT_USERNAME="$(bashio::services 'mqtt' 'username' || true)"
    export RELAY_MQTT_PASSWORD="$(bashio::services 'mqtt' 'password' || true)"
  else
    bashio::log.info "No MQTT broker: install the Mosquitto broker app to see your trackers in Home Assistant without the Ciconia integration."
  fi
fi

# Home Assistant reaches the app by its host name on the internal network.
host="$(hostname)"
export RELAY_HA_URL="http://${host}:8080"

# The link Home Assistant shows while connecting opens this app's panel,
# which asks to allow it.
slug="$(bashio::api.supervisor GET /addons/self/info false '.slug' 2>/dev/null || true)"
if [ -z "${slug}" ] || [ "${slug}" = "null" ]; then
  slug="${host//-/_}"
fi
export RELAY_APPROVE_URL="/hassio/ingress/${slug}"
# The "Visit" link on each tracker's device page opens this panel.
export RELAY_DEVICE_URL="homeassistant://hassio/ingress/${slug}"

# The Loxone feed and template (not shown in the panel here, but they work)
# need this machine's address on the home network and the port 8080 is
# published on (off until it's set under Configuration → Network).
port="$(bashio::api.supervisor GET /addons/self/info false '.network["8080/tcp"] // empty' 2>/dev/null || true)"
ip="$(bashio::api.supervisor GET /network/info false '[.interfaces[] | select(.primary)][0].ipv4.address[0] // empty' 2>/dev/null || true)"
if [ -n "${port}" ] && [ "${port}" != "null" ]; then
  if [ -n "${ip}" ] && [ "${ip}" != "null" ]; then
    export RELAY_LOXONE_URL="http://${ip%%/*}:${port}"
  else
    export RELAY_LOXONE_PORT="${port}"
  fi
fi

bashio::log.info "Trackers: ports 7171 (TCP) and 7172 (TLS). Home Assistant: ${RELAY_HA_URL}. MQTT: ${RELAY_MQTT_URL:-off}"
exec /usr/bin/relay
