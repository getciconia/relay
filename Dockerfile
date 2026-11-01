# Ciconia Private Relay: one static binary in a small image.
#   docker build -t ciconia-relay .
#   docker run -d -p 7171:7171 -p 7172:7172 -p 8080:8080 -v ciconia-relay:/data ciconia-relay
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /relay ./cmd/relay \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /fake-tracker ./cmd/fake-tracker

FROM alpine:3.22
RUN adduser -D -H -u 10001 relay \
 && mkdir /data && chown relay /data
COPY --from=build /relay /fake-tracker /usr/local/bin/
USER relay
ENV RELAY_DB=/data/relay.db \
    RELAY_TRACKER_ADDR=:7171 \
    RELAY_TRACKER_TLS_ADDR=:7172 \
    RELAY_HTTP_ADDR=:8080
VOLUME /data
# 7171 (TCP) and 7172 (TLS): trackers, the only ports to open to the internet.
# 8080: panel, Home Assistant, Loxone; keep it on your home network.
EXPOSE 7171 7172 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["relay"]
