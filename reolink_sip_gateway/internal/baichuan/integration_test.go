package baichuan

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type testWireRequest struct {
	Header    Header
	Extension []byte
	Body      []byte
}

func TestClientTalkRoundTripAgainstMockNVR(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		nonce := "ABCDEF0123456789"
		aesKey := DeriveAESKey(nonce, "secret")

		req, err := readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDLogin || req.Header.Class != classLegacy || req.Header.ResponseCode != 0xDC12 {
			serverErr <- fmt.Errorf("unexpected nonce request header: %+v", req.Header)
			return
		}
		nonceXML := []byte(`<?xml version="1.0" encoding="UTF-8"?><body><Encryption version="1.1"><type>AES</type><nonce>` + nonce + `</nonce></Encryption></body>`)
		if err := writeTestResponse(conn, Header{MsgID: msgIDLogin, MsgNum: req.Header.MsgNum, ResponseCode: 0xDD02, Class: classModern}, nil, nonceXML, EncryptionBC, [16]byte{}, false); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDLogin || req.Header.Class != classModernWithOffset {
			serverErr <- fmt.Errorf("unexpected login request: %+v", req.Header)
			return
		}
		loginXML := string(BCXOR(req.Header.ChannelID, req.Body))
		if !strings.Contains(loginXML, MD5Modern("admin"+nonce)) || !strings.Contains(loginXML, MD5Modern("secret"+nonce)) {
			serverErr <- fmt.Errorf("login hashes missing from request XML: %q", loginXML)
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDLogin, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDTalkAbility || req.Header.ChannelID != 1 {
			serverErr <- fmt.Errorf("unexpected ability request: %+v", req.Header)
			return
		}
		extPlain := decryptXML(req.Header.ChannelID, req.Extension, EncryptionAES, aesKey, true)
		if !strings.Contains(string(extPlain), "<channelId>1</channelId>") {
			serverErr <- fmt.Errorf("ability extension missing channel: %q", extPlain)
			return
		}
		abilityXML := []byte(`<?xml version="1.0" encoding="UTF-8"?><body><TalkAbility version="1.1"><duplexList><duplex>fullDuplex</duplex></duplexList><audioStreamModeList><audioStreamMode>speaker</audioStreamMode></audioStreamModeList><audioConfigList><audioConfig><audioType>adpcm</audioType><sampleRate>16000</sampleRate><samplePrecision>16</samplePrecision><lengthPerEncoder>1024</lengthPerEncoder><soundTrack>mono</soundTrack></audioConfig></audioConfigList></TalkAbility></body>`)
		if err := writeTestResponse(conn, Header{MsgID: msgIDTalkAbility, ChannelID: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, abilityXML, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDTalkConfig || req.Header.ChannelID != 1 {
			serverErr <- fmt.Errorf("unexpected talk config request: %+v", req.Header)
			return
		}
		cfgXML := string(decryptXML(req.Header.ChannelID, req.Body, EncryptionAES, aesKey, true))
		if !strings.Contains(cfgXML, "<audioType>adpcm</audioType>") || !strings.Contains(cfgXML, "<lengthPerEncoder>1024</lengthPerEncoder>") {
			serverErr <- fmt.Errorf("unexpected talk config XML: %q", cfgXML)
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDTalkConfig, ChannelID: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDTalk || req.Header.ChannelID != 1 {
			serverErr <- fmt.Errorf("unexpected talk data request: %+v", req.Header)
			return
		}
		extPlain = decryptXML(req.Header.ChannelID, req.Extension, EncryptionAES, aesKey, true)
		if !strings.Contains(string(extPlain), "<binaryData>1</binaryData>") {
			serverErr <- fmt.Errorf("binary extension missing: %q", extPlain)
			return
		}
		if len(req.Body) < 12 || binary.LittleEndian.Uint32(req.Body[:4]) != bcmediaADPCM {
			serverErr <- fmt.Errorf("invalid talk media payload: %x", req.Body)
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDTalkReset || req.Header.ChannelID != 1 {
			serverErr <- fmt.Errorf("unexpected talk reset request: %+v", req.Header)
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDTalkReset, ChannelID: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	addr := ln.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, Config{Host: "127.0.0.1", Port: addr.Port, Username: "admin", Password: "secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.StartTalk(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if session.SampleRate() != 16000 || session.SamplesPerBlock() != 1024 || session.BytesPerBlock() != 516 || session.Duplex() != "fullDuplex" {
		t.Fatalf("unexpected talk session: rate=%d samples=%d bytes=%d duplex=%q", session.SampleRate(), session.SamplesPerBlock(), session.BytesPerBlock(), session.Duplex())
	}
	block := make([]byte, session.BytesPerBlock())
	if err := session.WriteADPCMBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func readTestRequest(r io.Reader) (testWireRequest, error) {
	hbuf := make([]byte, 20)
	if _, err := io.ReadFull(r, hbuf); err != nil {
		return testWireRequest{}, err
	}
	if binary.LittleEndian.Uint32(hbuf[0:4]) != magicHeader {
		return testWireRequest{}, fmt.Errorf("bad magic")
	}
	h := Header{MsgID: binary.LittleEndian.Uint32(hbuf[4:8]), BodyLen: binary.LittleEndian.Uint32(hbuf[8:12]), ChannelID: hbuf[12], StreamType: hbuf[13], MsgNum: binary.LittleEndian.Uint16(hbuf[14:16]), ResponseCode: binary.LittleEndian.Uint16(hbuf[16:18]), Class: binary.LittleEndian.Uint16(hbuf[18:20])}
	if h.HasPayloadOffset() {
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return testWireRequest{}, err
		}
		h.PayloadOffset = binary.LittleEndian.Uint32(b)
	}
	if h.BodyLen > 2*1024*1024 {
		return testWireRequest{}, fmt.Errorf("body too large")
	}
	body := make([]byte, h.BodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return testWireRequest{}, err
	}
	if h.PayloadOffset > h.BodyLen {
		return testWireRequest{}, fmt.Errorf("bad offset")
	}
	return testWireRequest{Header: h, Extension: append([]byte(nil), body[:h.PayloadOffset]...), Body: append([]byte(nil), body[h.PayloadOffset:]...)}, nil
}

func writeTestResponse(w io.Writer, h Header, extension, body []byte, mode EncryptionMode, key [16]byte, hasKey bool) error {
	ext := append([]byte(nil), extension...)
	payload := append([]byte(nil), body...)
	if h.Class != classLegacy {
		if len(ext) > 0 {
			ext = encryptXML(h.ChannelID, ext, mode, key, hasKey)
		}
		if len(payload) > 0 {
			payload = encryptXML(h.ChannelID, payload, mode, key, hasKey)
		}
	} else if mode == EncryptionBC && len(payload) > 0 {
		payload = BCXOR(h.ChannelID, payload)
	}
	headerLen := 20
	if h.HasPayloadOffset() {
		headerLen = 24
	}
	pkt := make([]byte, headerLen+len(ext)+len(payload))
	binary.LittleEndian.PutUint32(pkt[0:4], magicHeader)
	binary.LittleEndian.PutUint32(pkt[4:8], h.MsgID)
	binary.LittleEndian.PutUint32(pkt[8:12], uint32(len(ext)+len(payload)))
	pkt[12] = h.ChannelID
	pkt[13] = h.StreamType
	binary.LittleEndian.PutUint16(pkt[14:16], h.MsgNum)
	binary.LittleEndian.PutUint16(pkt[16:18], h.ResponseCode)
	binary.LittleEndian.PutUint16(pkt[18:20], h.Class)
	if headerLen == 24 {
		binary.LittleEndian.PutUint32(pkt[20:24], uint32(len(ext)))
	}
	copy(pkt[headerLen:], ext)
	copy(pkt[headerLen+len(ext):], payload)
	_, err := w.Write(pkt)
	return err
}

func TestClientPreviewReceivesBinaryMediaAgainstMockNVR(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		nonce := "ABCDEF0123456789"
		aesKey := DeriveAESKey(nonce, "secret")

		req, err := readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		nonceXML := []byte(`<?xml version="1.0" encoding="UTF-8"?><body><Encryption version="1.1"><type>AES</type><nonce>` + nonce + `</nonce></Encryption></body>`)
		if err := writeTestResponse(conn, Header{MsgID: msgIDLogin, MsgNum: req.Header.MsgNum, ResponseCode: 0xDD02, Class: classModern}, nil, nonceXML, EncryptionBC, [16]byte{}, false); err != nil {
			serverErr <- err
			return
		}
		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDLogin, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDVideo || req.Header.ChannelID != 1 || req.Header.StreamType != 1 {
			serverErr <- fmt.Errorf("unexpected preview request: %+v", req.Header)
			return
		}
		previewXML := string(decryptXML(req.Header.ChannelID, req.Body, EncryptionAES, aesKey, true))
		if !strings.Contains(previewXML, "<handle>256</handle>") || !strings.Contains(previewXML, "<streamType>subStream</streamType>") {
			serverErr <- fmt.Errorf("unexpected preview XML: %q", previewXML)
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDVideo, ChannelID: 1, StreamType: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		ext, err := buildTalkExtension(1, true)
		if err != nil {
			serverErr <- err
			return
		}
		block := make([]byte, 516)
		binary.LittleEndian.PutUint16(block[0:2], uint16(int16(123)))
		block[2] = 10
		adpcmMedia := serializeTalkADPCMBlock(block, 7)
		videoPayload := []byte{0, 0, 0, 1, 0x65, 1, 2, 3, 4}
		videoMedia := make([]byte, 24+len(videoPayload)+padLen(len(videoPayload)))
		binary.LittleEndian.PutUint32(videoMedia[0:4], bcmediaIFrameMin)
		copy(videoMedia[4:8], []byte("H264"))
		binary.LittleEndian.PutUint32(videoMedia[8:12], uint32(len(videoPayload)))
		binary.LittleEndian.PutUint32(videoMedia[16:20], 12345)
		copy(videoMedia[24:], videoPayload)
		media := append(videoMedia, adpcmMedia...)
		if err := writeTestBinaryResponse(conn, Header{MsgID: msgIDVideo, ChannelID: 1, StreamType: 1, MsgNum: 500, ResponseCode: 200, Class: classModernWithOffset}, ext, media, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}

		req, err = readTestRequest(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if req.Header.MsgID != msgIDVideoStop || req.Header.ChannelID != 1 || req.Header.StreamType != 1 {
			serverErr <- fmt.Errorf("unexpected preview stop request: %+v", req.Header)
			return
		}
		if err := writeTestResponse(conn, Header{MsgID: msgIDVideoStop, ChannelID: 1, StreamType: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, aesKey, true); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	addr := ln.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, Config{Host: "127.0.0.1", Port: addr.Port, Username: "admin", Password: "secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	reader, err := client.StartAudioPreview(ctx, 1, StreamSub)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-reader.Packets:
		if packet.Kind != MediaPacketADPCM || len(packet.Data) != 516 {
			t.Fatalf("unexpected media packet: kind=%v len=%d", packet.Kind, len(packet.Data))
		}
		if got := int16(binary.LittleEndian.Uint16(packet.Data[:2])); got != 123 {
			t.Fatalf("unexpected predictor %d", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for preview media")
	}
	reader.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func writeTestBinaryResponse(w io.Writer, h Header, extension, body []byte, mode EncryptionMode, key [16]byte, hasKey bool) error {
	ext := append([]byte(nil), extension...)
	if h.Class != classLegacy && len(ext) > 0 {
		ext = encryptXML(h.ChannelID, ext, mode, key, hasKey)
	}
	headerLen := 20
	if h.HasPayloadOffset() {
		headerLen = 24
	}
	pkt := make([]byte, headerLen+len(ext)+len(body))
	binary.LittleEndian.PutUint32(pkt[0:4], magicHeader)
	binary.LittleEndian.PutUint32(pkt[4:8], h.MsgID)
	binary.LittleEndian.PutUint32(pkt[8:12], uint32(len(ext)+len(body)))
	pkt[12] = h.ChannelID
	pkt[13] = h.StreamType
	binary.LittleEndian.PutUint16(pkt[14:16], h.MsgNum)
	binary.LittleEndian.PutUint16(pkt[16:18], h.ResponseCode)
	binary.LittleEndian.PutUint16(pkt[18:20], h.Class)
	if headerLen == 24 {
		binary.LittleEndian.PutUint32(pkt[20:24], uint32(len(ext)))
	}
	copy(pkt[headerLen:], ext)
	copy(pkt[headerLen+len(ext):], body)
	_, err := w.Write(pkt)
	return err
}
