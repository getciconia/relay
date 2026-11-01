# Ciconia Relay as a Home Assistant app. The build context is the folder
# make-app.sh puts together: this file, run.sh and the relay's source in src/.
ARG BUILD_FROM=ghcr.io/home-assistant/base:3.24

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ .
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o /relay ./cmd/relay

FROM ${BUILD_FROM}
COPY --from=build /relay /usr/bin/relay
COPY run.sh /run.sh
RUN chmod a+x /run.sh

# The Supervisor restarts the app when this fails.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

CMD ["/run.sh"]

LABEL \
    org.opencontainers.image.title="Home Assistant App: Ciconia Relay" \
    org.opencontainers.image.description="Receive your GPS trackers yourself and see them in Home Assistant." \
    org.opencontainers.image.source="https://github.com/getciconia/relay" \
    org.opencontainers.image.licenses="MIT"
