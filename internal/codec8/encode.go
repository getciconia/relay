package codec8

import (
	"encoding/binary"
	"math"
	"sort"
)

// IMEIGreeting is what a tracker sends first.
func IMEIGreeting(imei string) []byte {
	out := binary.BigEndian.AppendUint16(nil, uint16(len(imei)))
	return append(out, imei...)
}

// EncodePacket builds an AVL data packet, the way a tracker sends it. Used by
// the fake tracker and the tests.
func EncodePacket(codec byte, records []Record) []byte {
	ext := codec == Codec8Extended
	body := []byte{codec, byte(len(records))}
	for _, rec := range records {
		body = binary.BigEndian.AppendUint64(body, uint64(rec.Time.UnixMilli()))
		body = append(body, rec.Priority)
		body = binary.BigEndian.AppendUint32(body, uint32(int32(math.Round(rec.Longitude*1e7))))
		body = binary.BigEndian.AppendUint32(body, uint32(int32(math.Round(rec.Latitude*1e7))))
		body = binary.BigEndian.AppendUint16(body, uint16(rec.Altitude))
		body = binary.BigEndian.AppendUint16(body, rec.Angle)
		body = append(body, rec.Satellites)
		body = binary.BigEndian.AppendUint16(body, rec.Speed)

		putN := func(n int) {
			if ext {
				body = binary.BigEndian.AppendUint16(body, uint16(n))
			} else {
				body = append(body, byte(n))
			}
		}
		ids := make([]int, 0, len(rec.IO))
		for id := range rec.IO {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		putN(int(rec.EventIO))
		putN(len(ids))
		for _, size := range []int{1, 2, 4, 8, 0} {
			if size == 0 && !ext {
				break
			}
			var group []int
			for _, id := range ids {
				if rec.IO[uint16(id)].Size == size {
					group = append(group, id)
				}
			}
			putN(len(group))
			for _, id := range group {
				putN(id)
				v := rec.IO[uint16(id)]
				if size == 0 {
					body = binary.BigEndian.AppendUint16(body, uint16(len(v.Bytes)))
					body = append(body, v.Bytes...)
					continue
				}
				for i := size - 1; i >= 0; i-- {
					body = append(body, byte(v.Raw>>(8*i)))
				}
			}
		}
	}
	body = append(body, byte(len(records)))

	out := make([]byte, 4, 12+len(body))
	out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	out = append(out, body...)
	return binary.BigEndian.AppendUint32(out, uint32(CRC16(body)))
}

// U8, U16, U32 and U64 make fixed-size IO values.
func U8(v uint8) IOValue   { return IOValue{Size: 1, Raw: uint64(v)} }
func U16(v uint16) IOValue { return IOValue{Size: 2, Raw: uint64(v)} }
func U32(v uint32) IOValue { return IOValue{Size: 4, Raw: uint64(v)} }
func U64(v uint64) IOValue { return IOValue{Size: 8, Raw: v} }
