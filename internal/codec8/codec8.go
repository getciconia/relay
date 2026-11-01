// Package codec8 reads and writes Teltonika Codec 8 and Codec 8 Extended
// over TCP: the IMEI greeting, AVL data packets and their CRC.
//
// A session: the tracker sends its IMEI (2-byte length + ASCII digits), the
// server answers 0x01 to accept or 0x00 to refuse, then the tracker sends AVL
// packets and the server answers each with the number of records it took
// (4 bytes, big-endian). A tracker that gets no or a lower count sends the
// records again later.
package codec8

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Codec IDs.
const (
	Codec8         byte = 0x08
	Codec8Extended byte = 0x8E
)

// MaxPacket caps the data length a packet may announce, so a bad or hostile
// client can't make the server allocate gigabytes.
const MaxPacket = 1 << 16

var (
	ErrBadIMEI     = errors.New("codec8: bad IMEI greeting")
	ErrBadPreamble = errors.New("codec8: packet doesn't start with 4 zero bytes")
	ErrTooLarge    = errors.New("codec8: packet too large")
	ErrBadCRC      = errors.New("codec8: CRC mismatch")
	ErrBadCount    = errors.New("codec8: record counts don't match")
)

// IOValue is one IO element. Fixed-size values (1, 2, 4 or 8 bytes) are in
// Raw; variable-length ones (Codec 8E "NX") are in Bytes with Size 0.
type IOValue struct {
	Size  int
	Raw   uint64
	Bytes []byte
}

// Unsigned is the value as an unsigned number (0 for variable-length ones).
func (v IOValue) Unsigned() uint64 { return v.Raw }

// Signed is the value read as a two's-complement number of its own size.
func (v IOValue) Signed() int64 {
	switch v.Size {
	case 1:
		return int64(int8(v.Raw))
	case 2:
		return int64(int16(v.Raw))
	case 4:
		return int64(int32(v.Raw))
	default:
		return int64(v.Raw)
	}
}

// Record is one AVL record: a position with its IO elements.
type Record struct {
	Time       time.Time
	Priority   uint8
	Longitude  float64 // degrees
	Latitude   float64 // degrees
	Altitude   int16   // metres
	Angle      uint16  // degrees from north
	Satellites uint8
	Speed      uint16 // km/h
	EventIO    uint16
	IO         map[uint16]IOValue
}

// ReadIMEI reads the greeting a tracker sends first.
func ReadIMEI(r io.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", err
	}
	if n == 0 || n > 32 {
		return "", ErrBadIMEI
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	for _, c := range buf {
		if c < '0' || c > '9' {
			return "", ErrBadIMEI
		}
	}
	return string(buf), nil
}

// Packet is one decoded AVL data packet.
type Packet struct {
	Codec   byte
	Records []Record
	Raw     []byte // the whole packet as received, for debugging
}

// ReadPacket reads one AVL data packet and checks its CRC.
func ReadPacket(r *bufio.Reader) (*Packet, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(head[:4]) != 0 {
		return nil, ErrBadPreamble
	}
	length := binary.BigEndian.Uint32(head[4:])
	if length < 3 || length > MaxPacket {
		return nil, ErrTooLarge
	}
	data := make([]byte, length+4)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	body, crc := data[:length], binary.BigEndian.Uint32(data[length:])
	if uint32(CRC16(body)) != crc {
		return nil, ErrBadCRC
	}
	codec, records, err := decodeBody(body)
	if err != nil {
		return nil, err
	}
	raw := append(head[:], data...)
	return &Packet{Codec: codec, Records: records, Raw: raw}, nil
}

// Ack is what the server answers to a packet: how many records it took.
func Ack(count int) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(count))
}

// CRC16 is CRC-16/IBM (ARC): polynomial 0xA001 reflected, initial value 0.
func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return make([]byte, n)
	}
	if r.pos+n > len(r.b) {
		r.err = io.ErrUnexpectedEOF
		return make([]byte, n)
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out
}

func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.BigEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32 { return binary.BigEndian.Uint32(r.take(4)) }
func (r *reader) u64() uint64 { return binary.BigEndian.Uint64(r.take(8)) }

func decodeBody(body []byte) (byte, []Record, error) {
	r := &reader{b: body}
	codec := r.u8()
	if codec != Codec8 && codec != Codec8Extended {
		return codec, nil, fmt.Errorf("codec8: unsupported codec 0x%02X", codec)
	}
	ext := codec == Codec8Extended
	count := int(r.u8())
	records := make([]Record, 0, count)
	for i := 0; i < count && r.err == nil; i++ {
		records = append(records, decodeRecord(r, ext))
	}
	if r.err != nil {
		return codec, nil, r.err
	}
	if int(r.u8()) != count || r.err != nil || r.pos != len(body) {
		return codec, nil, ErrBadCount
	}
	return codec, records, nil
}

func decodeRecord(r *reader, ext bool) Record {
	rec := Record{
		Time:       time.UnixMilli(int64(r.u64())).UTC(),
		Priority:   r.u8(),
		Longitude:  float64(int32(r.u32())) / 1e7,
		Latitude:   float64(int32(r.u32())) / 1e7,
		Altitude:   int16(r.u16()),
		Angle:      r.u16(),
		Satellites: r.u8(),
		Speed:      r.u16(),
		IO:         map[uint16]IOValue{},
	}
	id := func() uint16 {
		if ext {
			return r.u16()
		}
		return uint16(r.u8())
	}
	n := func() int {
		if ext {
			return int(r.u16())
		}
		return int(r.u8())
	}
	rec.EventIO = id()
	n() // total IO count; the groups below say the same
	for _, size := range []int{1, 2, 4, 8} {
		for i, c := 0, n(); i < c && r.err == nil; i++ {
			key := id()
			var v uint64
			b := r.take(size)
			for _, x := range b {
				v = v<<8 | uint64(x)
			}
			rec.IO[key] = IOValue{Size: size, Raw: v}
		}
	}
	if ext {
		for i, c := 0, n(); i < c && r.err == nil; i++ {
			key := id()
			length := int(r.u16())
			rec.IO[key] = IOValue{Bytes: append([]byte(nil), r.take(length)...)}
		}
	}
	return rec
}
