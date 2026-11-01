package codec8

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// The example packets from Teltonika's Codec 8 and Codec 8 Extended
// documentation.
const (
	exampleCodec8  = "000000000000003608010000016B40D8EA30010000000000000000000000000000000105021503010101425E0F01F10000601A014E0000000000000000010000C7CF"
	exampleCodec8E = "000000000000004A8E010000016B412CEE000100000000000000000000000000000000010005000100010100010011001D00010010015E2C880002000B000000003544C87A000E000000001DD7E06A00000100002994"
)

func read(t *testing.T, hexPacket string) *Packet {
	t.Helper()
	b, err := hex.DecodeString(hexPacket)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadPacket(bufio.NewReader(bytes.NewReader(b)))
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	return p
}

func TestCodec8Example(t *testing.T) {
	p := read(t, exampleCodec8)
	if p.Codec != Codec8 || len(p.Records) != 1 {
		t.Fatalf("codec %x, %d records", p.Codec, len(p.Records))
	}
	rec := p.Records[0]
	if want := time.Date(2019, 6, 10, 10, 4, 46, 0, time.UTC); !rec.Time.Equal(want) {
		t.Errorf("time %v, want %v", rec.Time, want)
	}
	if rec.Priority != 1 || rec.EventIO != 1 {
		t.Errorf("priority %d event %d", rec.Priority, rec.EventIO)
	}
	want := map[uint16]uint64{21: 3, 1: 1, 66: 0x5E0F, 241: 0x601A, 78: 0}
	for id, v := range want {
		if got := rec.IO[id].Unsigned(); got != v {
			t.Errorf("io%d = %d, want %d", id, got, v)
		}
	}
	if len(rec.IO) != len(want) {
		t.Errorf("%d IO elements, want %d", len(rec.IO), len(want))
	}
}

func TestCodec8ExtendedExample(t *testing.T) {
	p := read(t, exampleCodec8E)
	if p.Codec != Codec8Extended || len(p.Records) != 1 {
		t.Fatalf("codec %x, %d records", p.Codec, len(p.Records))
	}
	rec := p.Records[0]
	want := map[uint16]uint64{1: 1, 17: 0x1D, 16: 0x015E2C88, 11: 0x3544C87A, 14: 0x1DD7E06A}
	for id, v := range want {
		if got := rec.IO[id].Unsigned(); got != v {
			t.Errorf("io%d = %d, want %d", id, got, v)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	recs := []Record{{
		Time: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), Priority: 0,
		Longitude: 21.0122287, Latitude: 52.2296756, Altitude: -12, Angle: 270, Satellites: 9, Speed: 42,
		EventIO: 0,
		IO:      map[uint16]IOValue{21: U8(4), 69: U8(1), 70: U16(uint16(0xFFFF - 49)), 113: U8(87), 182: U16(12), 241: U32(26003), 16: U64(1234567)},
	}, {
		Time: time.Date(2026, 10, 31, 12, 0, 5, 0, time.UTC), Satellites: 0, IO: map[uint16]IOValue{},
	}}
	for _, codec := range []byte{Codec8, Codec8Extended} {
		packet := EncodePacket(codec, recs)
		p, err := ReadPacket(bufio.NewReader(bytes.NewReader(packet)))
		if err != nil {
			t.Fatalf("codec %x: %v", codec, err)
		}
		got := p.Records[0]
		if got.Latitude != 52.2296756 || got.Longitude != 21.0122287 || got.Altitude != -12 || got.Speed != 42 {
			t.Errorf("codec %x: position %+v", codec, got)
		}
		if got.IO[70].Signed() != -50 || got.IO[241].Unsigned() != 26003 || got.IO[16].Unsigned() != 1234567 {
			t.Errorf("codec %x: IO %+v", codec, got.IO)
		}
		if len(p.Records) != 2 || !p.Records[1].Time.Equal(recs[1].Time) {
			t.Errorf("codec %x: second record %+v", codec, p.Records)
		}
	}
}

func TestExtendedVariableLength(t *testing.T) {
	recs := []Record{{Time: time.UnixMilli(0).UTC(), IO: map[uint16]IOValue{385: {Bytes: []byte{1, 2, 3}}, 300: U8(5)}}}
	p, err := ReadPacket(bufio.NewReader(bytes.NewReader(EncodePacket(Codec8Extended, recs))))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Records[0].IO[385].Bytes; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Errorf("NX value %v", got)
	}
	if p.Records[0].IO[300].Unsigned() != 5 {
		t.Errorf("2-byte id lost: %+v", p.Records[0].IO)
	}
}

func TestRejectsBadPackets(t *testing.T) {
	good, _ := hex.DecodeString(exampleCodec8)
	cases := map[string][]byte{
		"bad crc":      append(append([]byte{}, good[:len(good)-1]...), 0x00),
		"bad preamble": append([]byte{0, 0, 0, 1}, good[4:]...),
		"too large":    {0, 0, 0, 0, 0x7F, 0, 0, 0},
	}
	for name, packet := range cases {
		if _, err := ReadPacket(bufio.NewReader(bytes.NewReader(packet))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	short := append([]byte{}, good[:20]...)
	if _, err := ReadPacket(bufio.NewReader(bytes.NewReader(short))); err == nil {
		t.Error("truncated packet accepted")
	}
}

func TestIMEI(t *testing.T) {
	imei, err := ReadIMEI(bytes.NewReader(IMEIGreeting("356307042441013")))
	if err != nil || imei != "356307042441013" {
		t.Fatalf("got %q, %v", imei, err)
	}
	if _, err := ReadIMEI(bytes.NewReader([]byte{0, 3, 'a', 'b', 'c'})); !errors.Is(err, ErrBadIMEI) {
		t.Errorf("letters accepted: %v", err)
	}
	if _, err := ReadIMEI(bytes.NewReader([]byte{0x10, 0})); !errors.Is(err, ErrBadIMEI) {
		t.Errorf("huge length accepted: %v", err)
	}
}

func TestAck(t *testing.T) {
	if !bytes.Equal(Ack(2), []byte{0, 0, 0, 2}) {
		t.Errorf("ack %v", Ack(2))
	}
}
