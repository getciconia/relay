// Package ingest is the public side of the relay: trackers connect over TCP
// (or TLS) and send Codec 8 / 8E packets. Only added trackers are accepted;
// an unknown IMEI is remembered as waiting and refused, so it keeps its
// records and sends them once it's added in the panel.
//
// The port faces the internet, so it's guarded: limited connections in total
// and per address, a short deadline for the greeting, IMEIs of exactly 15
// digits, a capped waiting list and only a few new IMEIs per address an hour.
package ingest

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/getciconia/relay/internal/codec8"
	"github.com/getciconia/relay/internal/store"
)

// Defaults for the limits; a zero field in Server uses these.
const (
	DefaultMaxConns      = 500
	DefaultMaxConnsPerIP = 20
	DefaultMaxWaiting    = 50
	DefaultNewPerIPHour  = 10
	GreetingTimeout      = 15 * time.Second
	IdleTimeout          = 15 * time.Minute // a connection that sends nothing for this long is closed
)

// Server accepts tracker connections. One Server can serve several
// listeners (plain TCP and TLS); the limits count both.
type Server struct {
	Store      *store.Store
	Log        *slog.Logger
	LogPackets bool // log every packet as hex, to capture a tracker's data

	MaxConns      int // connections at once, all addresses
	MaxConnsPerIP int // connections at once from one address
	MaxWaiting    int // waiting trackers kept; older ones make room
	NewPerIPHour  int // unknown IMEIs one address may introduce per hour

	once    sync.Once
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	perIP   map[string]int
	newByIP map[string][]time.Time
}

func (s *Server) init() {
	s.once.Do(func() {
		s.conns = map[net.Conn]struct{}{}
		s.perIP = map[string]int{}
		s.newByIP = map[string][]time.Time{}
		if s.MaxConns == 0 {
			s.MaxConns = DefaultMaxConns
		}
		if s.MaxConnsPerIP == 0 {
			s.MaxConnsPerIP = DefaultMaxConnsPerIP
		}
		if s.MaxWaiting == 0 {
			s.MaxWaiting = DefaultMaxWaiting
		}
		if s.NewPerIPHour == 0 {
			s.NewPerIPHour = DefaultNewPerIPHour
		}
	})
}

// Serve accepts connections from ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.init()
	go func() {
		<-ctx.Done()
		ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		ip := hostOf(conn.RemoteAddr())
		if !s.admit(conn, ip) {
			s.Log.Debug("too many connections; closed", "remote", ip)
			conn.Close()
			continue
		}
		go func() {
			defer s.release(conn, ip)
			s.handle(ctx, conn, ip)
		}()
	}
}

func (s *Server) admit(conn net.Conn, ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) >= s.MaxConns || s.perIP[ip] >= s.MaxConnsPerIP {
		return false
	}
	s.conns[conn] = struct{}{}
	s.perIP[ip]++
	return true
}

func (s *Server) release(conn net.Conn, ip string) {
	conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
	if s.perIP[ip]--; s.perIP[ip] <= 0 {
		delete(s.perIP, ip)
	}
}

// allowNew says whether ip may introduce another unknown IMEI this hour.
func (s *Server) allowNew(ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.newByIP[ip][:0]
	for _, t := range s.newByIP[ip] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= s.NewPerIPHour {
		s.newByIP[ip] = recent
		return false
	}
	s.newByIP[ip] = append(recent, now)
	if len(s.newByIP) > 10000 { // forget everyone rather than grow without end
		s.newByIP = map[string][]time.Time{ip: s.newByIP[ip]}
	}
	return true
}

func hostOf(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func validIMEI(imei string) bool {
	if len(imei) != 15 {
		return false
	}
	for _, c := range imei {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Server) handle(ctx context.Context, conn net.Conn, ip string) {
	log := s.Log.With("remote", ip)
	r := bufio.NewReader(conn)
	conn.SetDeadline(time.Now().Add(GreetingTimeout))
	imei, err := codec8.ReadIMEI(r)
	if err != nil || !validIMEI(imei) {
		log.Debug("not a tracker greeting; closed", "err", err)
		return
	}
	log = log.With("imei", imei)

	t, err := s.Store.TrackerByIMEI(ctx, imei)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !s.allowNew(ip, time.Now()) {
			log.Debug("too many new IMEIs from this address; refused")
			conn.Write([]byte{0x00})
			return
		}
		if t, err = s.Store.KnockLimited(ctx, imei, s.MaxWaiting); err != nil {
			log.Error("store", "err", err)
			return
		}
		log.Info("new tracker is waiting to be added in the panel; refused")
	case err != nil:
		log.Error("store", "err", err)
		return
	default:
		s.Store.Seen(ctx, t.ID)
	}
	if t.Status != store.Active {
		log.Debug("tracker is waiting to be added in the panel; refused")
		conn.Write([]byte{0x00})
		return
	}
	if _, err := conn.Write([]byte{0x01}); err != nil {
		return
	}
	log = log.With("tracker", t.Name)
	log.Info("connected")
	for {
		conn.SetDeadline(time.Now().Add(IdleTimeout))
		p, err := codec8.ReadPacket(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				log.Info("connection closed", "err", err)
			}
			return
		}
		if s.LogPackets {
			log.Info("packet", "hex", hex.EncodeToString(p.Raw))
		}
		// The tracker may have been removed while connected.
		if cur, err := s.Store.TrackerByID(ctx, t.ID); err != nil || cur.Status != store.Active {
			log.Info("tracker removed; closing")
			return
		}
		if err := s.Store.SavePositions(ctx, t.ID, p.Records); err != nil {
			log.Error("saving positions", "err", err)
			conn.Write(codec8.Ack(0)) // the tracker keeps them and tries again
			return
		}
		if _, err := conn.Write(codec8.Ack(len(p.Records))); err != nil {
			return
		}
		log.Debug("records", "count", len(p.Records))
	}
}
