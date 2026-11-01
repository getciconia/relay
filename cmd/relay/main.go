// Command relay is the Ciconia self-hosted relay: trackers send it their
// positions (Teltonika Codec 8 over TCP), and Home Assistant and Loxone read
// them from it, the same way they read Ciconia Cloud.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in a bare container

	"github.com/getciconia/relay/internal/api"
	"github.com/getciconia/relay/internal/config"
	"github.com/getciconia/relay/internal/hass"
	"github.com/getciconia/relay/internal/ingest"
	"github.com/getciconia/relay/internal/mqtt"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tlscert"
	"github.com/getciconia/relay/internal/web"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("database %s: %w", cfg.DBPath, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	trackerLn, err := net.Listen("tcp", cfg.TrackerAddr)
	if err != nil {
		return fmt.Errorf("tracker port: %w", err)
	}
	var tlsLn net.Listener
	var certs *tlscert.Source
	if cfg.TrackerTLSAddr != "" {
		certs, err = tlscert.Load(cfg.TLSCert, cfg.TLSKey, filepath.Dir(cfg.DBPath), cfg.TLSHosts)
		if err != nil {
			return err
		}
		if tlsLn, err = net.Listen("tcp", cfg.TrackerTLSAddr); err != nil {
			return fmt.Errorf("tracker TLS port: %w", err)
		}
		tlsLn = tls.NewListener(tlsLn, certs.Config())
	}
	httpLn, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("http port: %w", err)
	}

	mux := http.NewServeMux()
	(&api.API{Store: st, Log: log, PanelURL: cfg.PanelURL, ApproveURL: cfg.ApproveURL}).Register(mux)
	panel := &web.Panel{
		Store: st, Log: log, Version: version, MapTiles: cfg.MapTiles,
		TrackerPort: port(cfg.TrackerAddr), TLS: certs, HAURL: cfg.HAURL, LoxoneURL: cfg.LoxoneURL, LoxonePort: cfg.LoxonePort,
		HAApp: cfg.HAApp,
	}
	if cfg.HAApp && cfg.SupervisorToken != "" {
		panel.HADevices = &hass.Devices{URL: hass.SupervisorAPI, Token: cfg.SupervisorToken, Log: log}
	}
	if tlsLn != nil {
		panel.TLSPort = port(cfg.TrackerTLSAddr)
	}
	var publisher *mqtt.Publisher
	if cfg.MQTTURL != "" {
		publisher = &mqtt.Publisher{Store: st, Log: log, Config: mqtt.Config{
			URL: cfg.MQTTURL, Username: cfg.MQTTUsername, Password: cfg.MQTTPassword,
			Topic: cfg.MQTTTopic, DiscoveryPrefix: cfg.MQTTDiscoveryPrefix, DeviceURL: cfg.DeviceURL, Version: version,
		}}
		panel.MQTT = publisher
	}
	if err := panel.Register(mux); err != nil {
		return err
	}
	srv := &http.Server{Handler: web.SameOrigin(mux), ReadHeaderTimeout: 10 * time.Second}

	trackers := &ingest.Server{
		Store: st, Log: log, LogPackets: cfg.LogPackets,
		MaxConns: cfg.MaxConns, MaxConnsPerIP: cfg.MaxConnsPerIP, MaxWaiting: cfg.MaxWaiting, NewPerIPHour: cfg.NewPerIPHour,
	}
	errs := make(chan error, 4)
	mqttDone := make(chan struct{})
	if publisher != nil {
		go func() {
			defer close(mqttDone)
			if err := publisher.Run(ctx); err != nil {
				errs <- fmt.Errorf("mqtt: %w", err)
			}
		}()
	} else {
		close(mqttDone)
	}
	go func() { errs <- trackers.Serve(ctx, trackerLn) }()
	if tlsLn != nil {
		go func() { errs <- trackers.Serve(ctx, tlsLn) }()
	}
	go func() {
		if err := srv.Serve(httpLn); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	go prune(ctx, st, cfg.Retention, log)

	tlsAddr := "off"
	if tlsLn != nil {
		tlsAddr = cfg.TrackerTLSAddr
		if info, err := certs.Info(); err == nil {
			log.Info("tracker TLS certificate", "self_signed", info.SelfSigned, "names", strings.Join(info.Names, ","), "sha256", info.Fingerprint)
		}
	}
	log.Info("Ciconia Relay started", "version", version, "trackers", cfg.TrackerAddr, "trackers_tls", tlsAddr, "panel", cfg.HTTPAddr,
		"database", cfg.DBPath, "keep_positions", cfg.Retention.String(), "mqtt", cfg.MQTTURL != "")

	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	select { // let MQTT say offline
	case <-mqttDone:
	case <-shutdown.Done():
	}
	log.Info("stopped")
	return err
}

// prune deletes old positions now and every hour.
func prune(ctx context.Context, st *store.Store, keep time.Duration, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := st.Prune(ctx, keep); err != nil && ctx.Err() == nil {
			log.Error("pruning positions", "err", err)
		} else if n > 0 {
			log.Info("pruned old positions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func port(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}
