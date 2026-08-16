// Portions adapted from Shareed2k/reolinkproxy (MIT License).
// Copyright (c) 2026 Roman Kredentser. See THIRD-PARTY-NOTICES.md.
package baichuan

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultPort    = 9000
	DefaultTimeout = 10 * time.Second

	magicHeader = 0x0ABCDEF0

	classLegacy           = 0x6514
	classModern           = 0x6614
	classModernWithOffset = 0x6414

	msgIDLogin       = 1
	msgIDVideo       = 3
	msgIDVideoStop   = 4
	msgIDTalkAbility = 10
	msgIDTalkReset   = 11
	msgIDPing        = 93
	msgIDTalkConfig  = 201
	msgIDTalk        = 202

	bcmediaADPCM       = 0x62773130
	bcmediaADPCMHeader = 0x0100
	bcmediaPadSize     = 8
)

type Stream string

const (
	StreamMain   Stream = "mainStream"
	StreamSub    Stream = "subStream"
	StreamExtern Stream = "externStream"
)

func ParseStream(v string) (Stream, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "main", "mainstream":
		return StreamMain, nil
	case "sub", "substream":
		return StreamSub, nil
	case "extern", "externstream":
		return StreamExtern, nil
	default:
		return "", fmt.Errorf("unsupported Baichuan preview stream %q", v)
	}
}

func (s Stream) ShortName() string {
	switch s {
	case StreamMain:
		return "main"
	case StreamSub:
		return "sub"
	case StreamExtern:
		return "extern"
	default:
		return string(s)
	}
}

type MediaPacketKind int

const (
	MediaPacketInfoV1 MediaPacketKind = iota + 1
	MediaPacketInfoV2
	MediaPacketIFrame
	MediaPacketPFrame
	MediaPacketAAC
	MediaPacketADPCM
)

func (k MediaPacketKind) String() string {
	switch k {
	case MediaPacketInfoV1:
		return "info-v1"
	case MediaPacketInfoV2:
		return "info-v2"
	case MediaPacketIFrame:
		return "iframe"
	case MediaPacketPFrame:
		return "pframe"
	case MediaPacketAAC:
		return "aac"
	case MediaPacketADPCM:
		return "adpcm"
	default:
		return "unknown"
	}
}

type MediaPacket struct {
	Kind               MediaPacketKind
	Codec              string
	Data               []byte
	TimestampMicrosecs uint32
	HasTimestamp       bool
	UnixTime           *time.Time
	Width              uint32
	Height             uint32
	FPS                uint8
}

type MediaReader struct {
	Packets  <-chan MediaPacket
	client   *Client
	channel  uint8
	stream   Stream
	stop     chan struct{}
	stopOnce func()
}

func (r *MediaReader) Close() {
	if r == nil {
		return
	}
	if r.stopOnce != nil {
		r.stopOnce()
	}
	if r.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = r.client.StopPreview(ctx, r.channel, r.stream)
	}
}

type EncryptionMode uint8

const (
	EncryptionNone EncryptionMode = iota
	EncryptionBC
	EncryptionAES
)

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	Timeout  time.Duration
}

func (c Config) normalized() Config {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

type Header struct {
	MsgID         uint32
	BodyLen       uint32
	ChannelID     uint8
	StreamType    uint8
	MsgNum        uint16
	ResponseCode  uint16
	Class         uint16
	PayloadOffset uint32
}

func (h Header) HasPayloadOffset() bool { return h.Class == classModernWithOffset || h.Class == 0 }
func (h Header) IsModern() bool         { return h.Class != classLegacy }

type Extension struct {
	BinaryData *int `xml:"binaryData"`
	ChannelID  *int `xml:"channelId"`
	EncryptLen *int `xml:"encryptLen"`
}

type Message struct {
	Header        Header
	Extension     []byte
	Payload       []byte
	XML           string
	Binary        bool
	ExtensionMeta *Extension
}

func (m *Message) success() error {
	if !m.Header.HasPayloadOffset() {
		return nil
	}
	switch m.Header.ResponseCode {
	case 200, 201, 300:
		return nil
	default:
		return &StatusError{MsgID: m.Header.MsgID, Code: m.Header.ResponseCode}
	}
}

type StatusError struct {
	MsgID uint32
	Code  uint16
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("baichuan cmd %d failed with status %d", e.MsgID, e.Code)
}

func padLen(size int) int {
	if size%bcmediaPadSize == 0 {
		return 0
	}
	return bcmediaPadSize - (size % bcmediaPadSize)
}
