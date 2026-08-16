package rtp

import (
	"encoding/binary"
	"errors"
)

type Packet struct {
	PayloadType uint8
	Marker      bool
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	Payload     []byte
}

func Parse(b []byte) (Packet, error) {
	if len(b) < 12 {
		return Packet{}, errors.New("RTP packet too short")
	}
	if b[0]>>6 != 2 {
		return Packet{}, errors.New("unsupported RTP version")
	}
	cc := int(b[0] & 0x0F)
	x := b[0]&0x10 != 0
	p := b[0]&0x20 != 0
	off := 12 + cc*4
	if len(b) < off {
		return Packet{}, errors.New("invalid CSRC list")
	}
	if x {
		if len(b) < off+4 {
			return Packet{}, errors.New("invalid extension")
		}
		extWords := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		off += 4 + extWords*4
		if len(b) < off {
			return Packet{}, errors.New("truncated extension")
		}
	}
	end := len(b)
	if p {
		pad := int(b[len(b)-1])
		if pad == 0 || pad > end-off {
			return Packet{}, errors.New("invalid padding")
		}
		end -= pad
	}
	payload := make([]byte, end-off)
	copy(payload, b[off:end])
	return Packet{
		PayloadType: b[1] & 0x7F,
		Marker:      b[1]&0x80 != 0,
		Sequence:    binary.BigEndian.Uint16(b[2:4]),
		Timestamp:   binary.BigEndian.Uint32(b[4:8]),
		SSRC:        binary.BigEndian.Uint32(b[8:12]),
		Payload:     payload,
	}, nil
}

func Marshal(p Packet) []byte {
	b := make([]byte, 12+len(p.Payload))
	b[0] = 0x80
	b[1] = p.PayloadType & 0x7F
	if p.Marker {
		b[1] |= 0x80
	}
	binary.BigEndian.PutUint16(b[2:4], p.Sequence)
	binary.BigEndian.PutUint32(b[4:8], p.Timestamp)
	binary.BigEndian.PutUint32(b[8:12], p.SSRC)
	copy(b[12:], p.Payload)
	return b
}

func SenderReport(ssrc, rtpTS, packetCount, octetCount uint32, ntpSec, ntpFrac uint32) []byte {
	b := make([]byte, 28)
	b[0] = 0x80
	b[1] = 200
	binary.BigEndian.PutUint16(b[2:4], 6)
	binary.BigEndian.PutUint32(b[4:8], ssrc)
	binary.BigEndian.PutUint32(b[8:12], ntpSec)
	binary.BigEndian.PutUint32(b[12:16], ntpFrac)
	binary.BigEndian.PutUint32(b[16:20], rtpTS)
	binary.BigEndian.PutUint32(b[20:24], packetCount)
	binary.BigEndian.PutUint32(b[24:28], octetCount)
	return b
}
