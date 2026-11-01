// Command fake-tracker pretends to be one or more Teltonika ATM700s: each
// connects to a relay, says its IMEI and sends a position now and then. For
// trying the relay without real trackers.
//
//	go run ./cmd/fake-tracker -addr localhost:7171            # one tracker, driving
//	go run ./cmd/fake-tracker -addr localhost:7171 -n 3       # driving, parked at the centre, parked elsewhere
//	go run ./cmd/fake-tracker -addr localhost:7172 -tls -ca ciconia-relay.pem   # over TLS
//
// A tracker the relay doesn't know is refused until it's added in the panel;
// the fake keeps trying every 10 seconds, like a real one.
package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/getciconia/relay/codec8"
)

// What each fake tracker does, in turn.
const (
	driving = iota // circles around the centre at 25-35 km/h
	home           // stands still at the centre
	away           // stands still 2 km east, battery low
)

var behaviours = []string{"driving", "at the centre", "parked 2 km away"}

type fake struct {
	addr      string
	imei      string
	behaviour int
	lat, lng  float64
	every     time.Duration
	count     int
	codec     byte
	tls       *tls.Config // nil: plain TCP
	log       *log.Logger
}

func main() {
	addr := flag.String("addr", "localhost:7171", "relay's tracker address")
	imei := flag.String("imei", "356307042441013", "IMEI of the first tracker; the others count up from it")
	n := flag.Int("n", 1, "how many trackers")
	lat := flag.Float64("lat", 52.2296756, "centre latitude")
	lng := flag.Float64("lng", 21.0122287, "centre longitude")
	every := flag.Duration("every", 10*time.Second, "time between positions")
	count := flag.Int("count", 0, "positions each tracker sends, 0 = forever")
	extended := flag.Bool("8e", false, "send Codec 8 Extended instead of Codec 8")
	useTLS := flag.Bool("tls", false, "connect with TLS (to the relay's TLS port)")
	ca := flag.String("ca", "", "with -tls: the certificate to trust (PEM); without it the certificate isn't checked")
	flag.Parse()

	var tlsConfig *tls.Config
	if *useTLS {
		tlsConfig = &tls.Config{InsecureSkipVerify: *ca == ""}
		if *ca != "" {
			pem, err := os.ReadFile(*ca)
			if err != nil {
				log.Fatal(err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				log.Fatalf("no certificate in %s", *ca)
			}
			// Trackers check the CA, not the name; do the same.
			tlsConfig.InsecureSkipVerify = true
			tlsConfig.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
				cert, err := x509.ParseCertificate(raw[0])
				if err != nil {
					return err
				}
				_, err = cert.Verify(x509.VerifyOptions{Roots: pool})
				return err
			}
		}
	}

	first, err := strconv.ParseUint(*imei, 10, 64)
	if err != nil {
		log.Fatalf("-imei must be digits: %v", err)
	}
	codec := codec8.Codec8
	if *extended {
		codec = codec8.Codec8Extended
	}
	var wg sync.WaitGroup
	for i := range *n {
		f := &fake{
			addr: *addr, imei: fmt.Sprintf("%015d", first+uint64(i)), behaviour: i % len(behaviours),
			lat: *lat, lng: *lng, every: *every, count: *count, codec: codec, tls: tlsConfig,
		}
		f.log = log.New(log.Writer(), f.imei+" ", log.LstdFlags)
		f.log.Printf("%s, sending to %s", behaviours[f.behaviour], f.addr)
		wg.Add(1)
		go func() { defer wg.Done(); f.run() }()
	}
	wg.Wait()
}

func (f *fake) run() {
	for sent := 0; f.count == 0 || sent < f.count; {
		n, err := f.session(sent)
		sent += n
		if err != nil {
			f.log.Printf("%v; trying again in 10 s", err)
			time.Sleep(10 * time.Second)
		}
	}
}

// session connects once and sends until the count is reached or the
// connection breaks; it returns how many positions went through.
func (f *fake) session(step int) (int, error) {
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if f.tls != nil {
		conn, err = tls.DialWithDialer(dialer, "tcp", f.addr, f.tls)
	} else {
		conn, err = dialer.Dial("tcp", f.addr)
	}
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	conn.Write(codec8.IMEIGreeting(f.imei))
	answer, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if answer != 0x01 {
		return 0, fmt.Errorf("refused: add %s in the relay's panel", f.imei)
	}
	f.log.Print("connected")
	sent := 0
	for {
		rec := f.record(step + sent)
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		if _, err := conn.Write(codec8.EncodePacket(f.codec, []codec8.Record{rec})); err != nil {
			return sent, err
		}
		ack := make([]byte, 4)
		if _, err := io.ReadFull(r, ack); err != nil {
			return sent, fmt.Errorf("no ACK: %w", err)
		}
		sent++
		f.log.Printf("sent %.5f, %.5f at %d km/h", rec.Latitude, rec.Longitude, rec.Speed)
		if f.count > 0 && step+sent >= f.count {
			return sent, nil
		}
		time.Sleep(f.every)
	}
}

func (f *fake) record(i int) codec8.Record {
	lat, lng, speed, battery := f.lat, f.lng, 0.0, 95-(i/10)%60
	switch f.behaviour {
	case driving:
		angle := float64(i) / 20 * 2 * math.Pi
		lat += 0.003 * math.Sin(angle)
		lng += 0.005 * math.Cos(angle)
		speed = 30 + 5*math.Sin(angle)
	case home:
		lat += 0.00005 * math.Sin(float64(i)) // GPS jitter
	case away:
		lng += 0.03 // about 2 km east
		battery = 15
	}
	return codec8.Record{
		Time:       time.Now().UTC(),
		Latitude:   lat,
		Longitude:  lng,
		Altitude:   110,
		Angle:      uint16(i * 18 % 360),
		Satellites: 9,
		Speed:      uint16(speed),
		IO: map[uint16]codec8.IOValue{
			21:  codec8.U8(4),                   // GSM signal
			69:  codec8.U8(1),                   // GNSS on, with fix
			70:  codec8.U16(uint16(230 + i%20)), // temperature, 0.1 °C
			113: codec8.U8(uint8(battery)),      // battery %
			182: codec8.U16(9),                  // HDOP 0.9
			241: codec8.U32(26003),              // operator
		},
	}
}
