package media

import "github.com/vothmarkus/reolink-sip-gateway/internal/rtp"

const g711SamplesPerPacket = 160 // 20 ms at 8 kHz; G.711 is one byte per sample.

// rtpRepacketizer converts an arbitrary sequence of G.711 RTP payload sizes
// into fixed 20 ms packets. FFmpeg's RTP muxer does not guarantee that a UDP
// pkt_size results in a fixed audio ptime, so the SIP leg must not inherit its
// packet boundaries.
type rtpRepacketizer struct {
	initialized     bool
	inputSSRC       uint32
	expectedInputTS uint32
	sequence        uint16
	timestamp       uint32
	ssrc            uint32
	markerNext      bool
	buffer          []byte
}

func (p *rtpRepacketizer) Push(in rtp.Packet, payloadType uint8) []rtp.Packet {
	if len(in.Payload) == 0 {
		return nil
	}
	// A timestamp discontinuity on the local FFmpeg RTP stream means packets
	// were lost or the encoder restarted. Drop a partial packet rather than
	// joining samples across the discontinuity.
	if !p.initialized || in.SSRC != p.inputSSRC || in.Timestamp != p.expectedInputTS {
		p.initialized = true
		p.inputSSRC = in.SSRC
		p.sequence = in.Sequence
		p.timestamp = in.Timestamp
		p.ssrc = in.SSRC
		p.markerNext = true
		p.buffer = p.buffer[:0]
	}
	p.expectedInputTS = in.Timestamp + uint32(len(in.Payload))
	p.buffer = append(p.buffer, in.Payload...)

	var out []rtp.Packet
	for len(p.buffer) >= g711SamplesPerPacket {
		payload := make([]byte, g711SamplesPerPacket)
		copy(payload, p.buffer[:g711SamplesPerPacket])
		p.buffer = p.buffer[g711SamplesPerPacket:]
		out = append(out, rtp.Packet{
			PayloadType: payloadType,
			Marker:      p.markerNext,
			Sequence:    p.sequence,
			Timestamp:   p.timestamp,
			SSRC:        p.ssrc,
			Payload:     payload,
		})
		p.markerNext = false
		p.sequence++
		p.timestamp += g711SamplesPerPacket
	}
	return out
}

// g711Chunker is used on the Reolink backchannel leg so arbitrary SIP ptime
// values are normalized to 20 ms before they are sent into ONVIF RTSP.
type g711Chunker struct {
	buffer []byte
}

func (c *g711Chunker) Push(payload []byte) [][]byte {
	if len(payload) == 0 {
		return nil
	}
	c.buffer = append(c.buffer, payload...)
	var out [][]byte
	for len(c.buffer) >= g711SamplesPerPacket {
		chunk := make([]byte, g711SamplesPerPacket)
		copy(chunk, c.buffer[:g711SamplesPerPacket])
		c.buffer = c.buffer[g711SamplesPerPacket:]
		out = append(out, chunk)
	}
	return out
}
