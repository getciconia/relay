package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/getciconia/relay/internal/codec8"
	"github.com/getciconia/relay/internal/store"
	"github.com/getciconia/relay/internal/tlscert"
)

func start(t *testing.T) (*store.Store, string) {
	t.Helper()
	return startWith(t, &Server{})
}

func startWith(t *testing.T, srv *Server) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Store, srv.Log = st, slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done; st.Close() })
	return st, ln.Addr().String()
}

func dial(t *testing.T, addr, imei string) (net.Conn, byte) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write(codec8.IMEIGreeting(imei))
	var answer [1]byte
	if _, err := io.ReadFull(c, answer[:]); err != nil {
		t.Fatal(err)
	}
	return c, answer[0]
}

func TestUnknownTrackerWaits(t *testing.T) {
	st, addr := start(t)
	_, answer := dial(t, addr, "356307042441013")
	if answer != 0x00 {
		t.Fatalf("unknown tracker accepted: %x", answer)
	}
	waiting, _ := st.WaitingTrackers(context.Background())
	if len(waiting) != 1 || waiting[0].IMEI != "356307042441013" {
		t.Fatalf("waiting %+v", waiting)
	}
}

func TestAddedTrackerSendsRecords(t *testing.T) {
	st, addr := start(t)
	ctx := context.Background()
	tr, _ := st.Knock(ctx, "356307042441013")
	st.AddTracker(ctx, tr.ID, "Car")

	c, answer := dial(t, addr, "356307042441013")
	if answer != 0x01 {
		t.Fatalf("added tracker refused: %x", answer)
	}
	recs := []codec8.Record{
		{Time: time.Now().Add(-time.Minute), Latitude: 52.2, Longitude: 21, Satellites: 8, IO: map[uint16]codec8.IOValue{113: codec8.U8(90)}},
		{Time: time.Now(), Latitude: 52.3, Longitude: 21, Satellites: 8, IO: map[uint16]codec8.IOValue{}},
	}
	for _, codec := range []byte{codec8.Codec8, codec8.Codec8Extended} {
		c.Write(codec8.EncodePacket(codec, recs))
		ack := make([]byte, 4)
		if _, err := io.ReadFull(c, ack); err != nil || !bytes.Equal(ack, codec8.Ack(2)) {
			t.Fatalf("codec %x ack %v %v", codec, ack, err)
		}
	}
	if n, _ := st.PositionCount(ctx, tr.ID); n != 4 {
		t.Errorf("stored %d positions", n)
	}
	latest, _ := st.LatestPosition(ctx, tr.ID)
	if latest.Latitude != 52.3 {
		t.Errorf("latest %+v", latest)
	}

	// A broken packet closes the connection without an ACK.
	bad := codec8.EncodePacket(codec8.Codec8, recs)
	bad[len(bad)-1] ^= 0xFF
	c.Write(bad)
	if _, err := io.ReadFull(c, make([]byte, 4)); err == nil {
		t.Error("bad CRC was acknowledged")
	}
	if n, _ := st.PositionCount(ctx, tr.ID); n != 4 {
		t.Errorf("bad packet stored: %d", n)
	}
}

func TestRemovedWhileConnected(t *testing.T) {
	st, addr := start(t)
	ctx := context.Background()
	tr, _ := st.Knock(ctx, "100000000000001")
	st.AddTracker(ctx, tr.ID, "Car")
	c, _ := dial(t, addr, "100000000000001")
	st.DeleteTracker(ctx, tr.ID)
	c.Write(codec8.EncodePacket(codec8.Codec8, []codec8.Record{{Time: time.Now(), IO: map[uint16]codec8.IOValue{}}}))
	if _, err := io.ReadFull(c, make([]byte, 4)); err == nil {
		t.Error("removed tracker's records were acknowledged")
	}
}

func TestGarbageGreeting(t *testing.T) {
	_, addr := start(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Errorf("answered %d bytes to garbage", n)
	}
}

func TestIMEIMustBe15Digits(t *testing.T) {
	st, addr := start(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write(codec8.IMEIGreeting("12345"))
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Errorf("answered %d bytes to a short IMEI", n)
	}
	if w, _ := st.WaitingTrackers(context.Background()); len(w) != 0 {
		t.Errorf("short IMEI stored: %+v", w)
	}
}

func TestNewIMEIsPerAddressAreLimited(t *testing.T) {
	st, addr := startWith(t, &Server{NewPerIPHour: 2})
	for i, want := range []byte{0x00, 0x00, 0x00} {
		_, answer := dial(t, addr, "35630704244100"+string(rune('0'+i)))
		if answer != want {
			t.Errorf("knock %d answered %x", i, answer)
		}
	}
	if w, _ := st.WaitingTrackers(context.Background()); len(w) != 2 {
		t.Errorf("waiting %d, want 2: the third new IMEI is over the limit", len(w))
	}
	// A known tracker isn't new, so the limit doesn't stop it.
	ctx := context.Background()
	known, _ := st.TrackerByIMEI(ctx, "356307042441000")
	st.AddTracker(ctx, known.ID, "Car")
	if _, answer := dial(t, addr, "356307042441000"); answer != 0x01 {
		t.Errorf("added tracker refused after the limit: %x", answer)
	}
}

func TestConnectionsPerAddressAreLimited(t *testing.T) {
	_, addr := startWith(t, &Server{MaxConnsPerIP: 2})
	var open []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		open = append(open, c)
	}
	time.Sleep(100 * time.Millisecond)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || err.Error() == "i/o timeout" {
		t.Errorf("third connection wasn't closed: %v", err)
	}
	open[0].Close()
	time.Sleep(100 * time.Millisecond)
	if _, answer := dial(t, addr, "356307042441013"); answer != 0x00 {
		t.Errorf("after one closed, a new connection should get through: %x", answer)
	}
}

func TestOverTLS(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	certs, err := tlscert.Load("", "", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go srv.Serve(ctx, tls.NewListener(raw, certs.Config()))

	tr, _ := st.Knock(ctx, "356307042441013")
	st.AddTracker(ctx, tr.ID, "Car")
	c, err := tls.Dial("tcp", raw.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write(codec8.IMEIGreeting("356307042441013"))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(c, answer); err != nil || answer[0] != 1 {
		t.Fatalf("greeting over TLS: %v %v", answer, err)
	}
	c.Write(codec8.EncodePacket(codec8.Codec8Extended, []codec8.Record{{Time: time.Now(), Latitude: 52, Longitude: 21, IO: map[uint16]codec8.IOValue{}}}))
	ack := make([]byte, 4)
	if _, err := io.ReadFull(c, ack); err != nil || !bytes.Equal(ack, codec8.Ack(1)) {
		t.Fatalf("ack over TLS %v %v", ack, err)
	}

	// Plain bytes on the TLS port get nowhere.
	p, _ := net.Dial("tcp", raw.Addr().String())
	defer p.Close()
	p.SetDeadline(time.Now().Add(3 * time.Second))
	p.Write(codec8.IMEIGreeting("356307042441013"))
	if n, _ := p.Read(make([]byte, 64)); n > 0 && n < 5 {
		t.Errorf("plain greeting on the TLS port answered %d bytes", n)
	}
}
