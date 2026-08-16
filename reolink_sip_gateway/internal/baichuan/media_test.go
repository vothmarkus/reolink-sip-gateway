package baichuan

import (
	"encoding/binary"
	"testing"
)

func TestMediaParserADPCMAndAAC(t *testing.T) {
	adpcmBlock := make([]byte, 516)
	binary.LittleEndian.PutUint16(adpcmBlock[0:2], 321)
	adpcm := serializeTalkADPCMBlock(adpcmBlock, 9)

	aacPayload := []byte{0xff, 0xf1, 0x60, 0x40, 0x01, 0x7f, 0xfc, 1, 2, 3, 4, 5}
	payloadSize := len(aacPayload)
	aac := make([]byte, 8+payloadSize+padLen(payloadSize))
	binary.LittleEndian.PutUint32(aac[0:4], bcmediaAAC)
	binary.LittleEndian.PutUint16(aac[4:6], uint16(payloadSize))
	copy(aac[8:], aacPayload)

	stream := append(append([]byte(nil), adpcm...), aac...)
	var parser MediaParser
	var packets []MediaPacket
	// Deliberately split the bcmedia stream at awkward boundaries to exercise
	// incremental parsing rather than only full-packet input.
	pos := 0
	for _, n := range []int{3, 8, 26, 149, 400, len(stream)} {
		if pos >= len(stream) {
			break
		}
		end := pos + n
		if end > len(stream) {
			end = len(stream)
		}
		got, err := parser.Append(stream[pos:end])
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, got...)
		pos = end
	}
	if pos < len(stream) {
		got, err := parser.Append(stream[pos:])
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, got...)
	}
	if len(packets) != 2 {
		t.Fatalf("packets=%d want=2", len(packets))
	}
	if packets[0].Kind != MediaPacketADPCM || len(packets[0].Data) != len(adpcmBlock) {
		t.Fatalf("bad ADPCM packet: kind=%v len=%d", packets[0].Kind, len(packets[0].Data))
	}
	if packets[1].Kind != MediaPacketAAC || string(packets[1].Data) != string(aacPayload) {
		t.Fatalf("bad AAC packet: kind=%v data=%x", packets[1].Kind, packets[1].Data)
	}
}

func TestParseStreamAliases(t *testing.T) {
	cases := map[string]Stream{"sub": StreamSub, "subStream": StreamSub, "main": StreamMain, "extern": StreamExtern}
	for in, want := range cases {
		got, err := ParseStream(in)
		if err != nil || got != want {
			t.Fatalf("ParseStream(%q)=%q,%v want=%q", in, got, err, want)
		}
	}
	if _, err := ParseStream("invalid"); err == nil {
		t.Fatal("invalid stream accepted")
	}
}

func TestMediaParserAudioOnlySkipsVideoPayload(t *testing.T) {
	videoPayload := []byte{0, 0, 0, 1, 0x65, 1, 2, 3, 4}
	video := make([]byte, 24+len(videoPayload)+padLen(len(videoPayload)))
	binary.LittleEndian.PutUint32(video[0:4], bcmediaIFrameMin)
	copy(video[4:8], []byte("H264"))
	binary.LittleEndian.PutUint32(video[8:12], uint32(len(videoPayload)))
	binary.LittleEndian.PutUint32(video[12:16], 0)
	binary.LittleEndian.PutUint32(video[16:20], 12345)
	copy(video[24:], videoPayload)

	aacPayload := []byte{0xff, 0xf1, 0x60, 0x40, 0x01, 0x7f, 0xfc, 1, 2, 3}
	aac := make([]byte, 8+len(aacPayload)+padLen(len(aacPayload)))
	binary.LittleEndian.PutUint32(aac[0:4], bcmediaAAC)
	binary.LittleEndian.PutUint16(aac[4:6], uint16(len(aacPayload)))
	copy(aac[8:], aacPayload)

	parser := MediaParser{AudioOnly: true}
	packets, err := parser.Append(append(video, aac...))
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 {
		t.Fatalf("audio-only packets=%d want=1", len(packets))
	}
	if packets[0].Kind != MediaPacketAAC || string(packets[0].Data) != string(aacPayload) {
		t.Fatalf("unexpected audio packet: kind=%v data=%x", packets[0].Kind, packets[0].Data)
	}
}
