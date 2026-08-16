// Portions adapted from Shareed2k/reolinkproxy (MIT License).
// Copyright (c) 2026 Roman Kredentser. See THIRD-PARTY-NOTICES.md.
package baichuan

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type UnsupportedTalkError struct{ Reason string }

func (e *UnsupportedTalkError) Error() string {
	return fmt.Sprintf("device does not support talkback: %s", e.Reason)
}

type TalkAudioConfig struct {
	Priority        *uint32 `xml:"priority,omitempty"`
	AudioType       string  `xml:"audioType"`
	SampleRate      uint16  `xml:"sampleRate"`
	SamplePrecision uint16  `xml:"samplePrecision"`
	LengthPerEncode uint16  `xml:"lengthPerEncoder"`
	SoundTrack      string  `xml:"soundTrack"`
}
type TalkConfig struct {
	Version         string          `xml:"version,attr"`
	ChannelID       uint8           `xml:"channelId"`
	Duplex          string          `xml:"duplex"`
	AudioStreamMode string          `xml:"audioStreamMode"`
	AudioConfig     TalkAudioConfig `xml:"audioConfig"`
}
type TalkAbility struct {
	Version             string                  `xml:"version,attr"`
	DuplexList          []talkDuplexOption      `xml:"duplexList"`
	AudioStreamModeList []talkAudioStreamMode   `xml:"audioStreamModeList"`
	AudioConfigList     []talkAudioConfigOption `xml:"audioConfigList"`
}
type talkDuplexOption struct {
	Duplex string `xml:"duplex"`
}
type talkAudioStreamMode struct {
	AudioStreamMode string `xml:"audioStreamMode"`
}
type talkAudioConfigOption struct {
	AudioConfig TalkAudioConfig `xml:"audioConfig"`
}
type talkAbilityBody struct {
	XMLName     xml.Name     `xml:"body"`
	TalkAbility *TalkAbility `xml:"TalkAbility"`
}
type talkConfigBody struct {
	XMLName    xml.Name    `xml:"body"`
	TalkConfig *TalkConfig `xml:"TalkConfig"`
}
type talkExtension struct {
	XMLName    xml.Name `xml:"Extension"`
	Version    string   `xml:"version,attr"`
	ChannelID  uint8    `xml:"channelId"`
	BinaryData *int     `xml:"binaryData,omitempty"`
}

type TalkSession struct {
	client          *Client
	channel         uint8
	binaryExtension []byte
	config          TalkConfig
	samplesPerBlock int
	bytesPerBlock   int
	mu              sync.Mutex
	closed          bool
	closeOnce       sync.Once
	seq             uint16
}

func (s *TalkSession) SampleRate() int         { return int(s.config.AudioConfig.SampleRate) }
func (s *TalkSession) SamplePrecision() int    { return int(s.config.AudioConfig.SamplePrecision) }
func (s *TalkSession) AudioType() string       { return s.config.AudioConfig.AudioType }
func (s *TalkSession) Duplex() string          { return s.config.Duplex }
func (s *TalkSession) AudioStreamMode() string { return s.config.AudioStreamMode }
func (s *TalkSession) SamplesPerBlock() int    { return s.samplesPerBlock }
func (s *TalkSession) BytesPerBlock() int      { return s.bytesPerBlock }

func (s *TalkSession) WriteADPCMBlock(ctx context.Context, block []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if len(block) != s.bytesPerBlock {
		return fmt.Errorf("unexpected adpcm block size %d, want %d", len(block), s.bytesPerBlock)
	}
	s.seq++
	payload := serializeTalkADPCMBlock(block, s.seq)
	return s.client.writeRequest(request{MsgID: msgIDTalk, ChannelID: s.channel, MsgNum: s.client.reserveMessageNumber(), Class: classModernWithOffset, Extension: s.binaryExtension, Body: payload, Binary: true})
}
func (s *TalkSession) Close(ctx context.Context) error {
	var err error
	s.closeOnce.Do(func() { s.mu.Lock(); s.closed = true; s.mu.Unlock(); err = s.client.stopTalk(ctx, s.channel) })
	return err
}

// PreferredTalkAudioProfile returns the device-advertised ADPCM talk profile
// without starting a talk session. The receive path uses its sample rate as a
// conservative hint for legacy Baichuan ADPCM preview packets, which do not
// carry an explicit sampling rate in every media frame.
func (c *Client) PreferredTalkAudioProfile(ctx context.Context, channel uint8) (TalkAudioConfig, error) {
	if err := c.Login(ctx); err != nil {
		return TalkAudioConfig{}, err
	}
	ability, err := c.getTalkAbility(ctx, channel)
	if err != nil {
		return TalkAudioConfig{}, err
	}
	cfg, err := defaultTalkConfig(channel, ability)
	if err != nil {
		return TalkAudioConfig{}, err
	}
	return cfg.AudioConfig, nil
}

func (c *Client) StartTalk(ctx context.Context, channel uint8) (*TalkSession, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	ability, err := c.getTalkAbility(ctx, channel)
	if err != nil {
		return nil, err
	}
	cfg, err := defaultTalkConfig(channel, ability)
	if err != nil {
		return nil, err
	}
	if err := c.startTalkSession(ctx, channel, cfg); err != nil {
		return nil, err
	}
	ext, err := buildTalkExtension(channel, true)
	if err != nil {
		_ = c.stopTalk(ctx, channel)
		return nil, err
	}
	samples := int(cfg.AudioConfig.LengthPerEncode)
	if samples < 2 || samples%2 != 0 {
		_ = c.stopTalk(ctx, channel)
		return nil, fmt.Errorf("invalid talk lengthPerEncoder %d", samples)
	}
	return &TalkSession{client: c, channel: channel, binaryExtension: ext, config: cfg, samplesPerBlock: samples, bytesPerBlock: samples/2 + 4}, nil
}

func (c *Client) getTalkAbility(ctx context.Context, channel uint8) (*TalkAbility, error) {
	ext, err := buildTalkExtension(channel, false)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendRequest(ctx, request{MsgID: msgIDTalkAbility, ChannelID: channel, Class: classModernWithOffset, Extension: ext})
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) {
			return nil, &UnsupportedTalkError{Reason: fmt.Sprintf("status %d from talk ability", se.Code)}
		}
		return nil, err
	}
	var body talkAbilityBody
	if err := xml.Unmarshal([]byte(resp.XML), &body); err != nil {
		return nil, fmt.Errorf("parse talk ability: %w", err)
	}
	if body.TalkAbility == nil {
		return nil, &UnsupportedTalkError{Reason: "talk ability missing from response"}
	}
	return body.TalkAbility, nil
}

func defaultTalkConfig(channel uint8, ability *TalkAbility) (TalkConfig, error) {
	if ability == nil {
		return TalkConfig{}, &UnsupportedTalkError{Reason: "empty talk ability"}
	}
	if len(ability.DuplexList) == 0 || len(ability.AudioStreamModeList) == 0 || len(ability.AudioConfigList) == 0 {
		return TalkConfig{}, &UnsupportedTalkError{Reason: "device returned no talk profiles"}
	}
	for _, opt := range ability.AudioConfigList {
		cfg := opt.AudioConfig
		if !strings.EqualFold(cfg.AudioType, "adpcm") || cfg.SampleRate == 0 || cfg.LengthPerEncode == 0 {
			continue
		}
		version := ability.Version
		if version == "" {
			version = "1.1"
		}
		cfg.Priority = nil
		duplex := ability.DuplexList[0].Duplex
		for _, d := range ability.DuplexList {
			if strings.EqualFold(d.Duplex, "fullDuplex") || strings.EqualFold(d.Duplex, "FDX") {
				duplex = d.Duplex
				break
			}
		}
		mode := ability.AudioStreamModeList[0].AudioStreamMode
		for _, m := range ability.AudioStreamModeList {
			if strings.EqualFold(m.AudioStreamMode, "speaker") {
				mode = m.AudioStreamMode
				break
			}
		}
		return TalkConfig{Version: version, ChannelID: channel, Duplex: duplex, AudioStreamMode: mode, AudioConfig: cfg}, nil
	}
	return TalkConfig{}, &UnsupportedTalkError{Reason: "device does not advertise ADPCM talk"}
}

func (c *Client) startTalkSession(ctx context.Context, channel uint8, cfg TalkConfig) error {
	ext, err := buildTalkExtension(channel, false)
	if err != nil {
		return err
	}
	body, err := marshalXMLDocument(talkConfigBody{TalkConfig: &cfg})
	if err != nil {
		return err
	}
	req := request{MsgID: msgIDTalkConfig, ChannelID: channel, Class: classModernWithOffset, Extension: ext, Body: body}
	resp, err := c.roundTripRequest(ctx, req)
	if err != nil {
		return err
	}
	if resp.Header.ResponseCode == 422 {
		if err := c.stopTalk(ctx, channel); err != nil {
			return err
		}
		resp, err = c.roundTripRequest(ctx, req)
		if err != nil {
			return err
		}
	}
	if err := resp.success(); err != nil {
		var se *StatusError
		if errors.As(err, &se) {
			return &UnsupportedTalkError{Reason: fmt.Sprintf("talk config rejected with status %d", se.Code)}
		}
		return err
	}
	return nil
}
func (c *Client) stopTalk(ctx context.Context, channel uint8) error {
	ext, err := buildTalkExtension(channel, false)
	if err != nil {
		return err
	}
	_, err = c.sendRequest(ctx, request{MsgID: msgIDTalkReset, ChannelID: channel, Class: classModernWithOffset, Extension: ext})
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == 422 {
			return nil
		}
		return err
	}
	return nil
}
func buildTalkExtension(channel uint8, binaryData bool) ([]byte, error) {
	ext := talkExtension{Version: "1.1", ChannelID: channel}
	if binaryData {
		v := 1
		ext.BinaryData = &v
	}
	return marshalXMLDocument(ext)
}
func serializeTalkADPCMBlock(block []byte, seq uint16) []byte {
	payloadSize := len(block) + 4
	total := 8 + payloadSize + padLen(payloadSize)
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], bcmediaADPCM)
	binary.LittleEndian.PutUint16(out[4:6], uint16(payloadSize))
	binary.LittleEndian.PutUint16(out[6:8], uint16(payloadSize))
	binary.LittleEndian.PutUint16(out[8:10], bcmediaADPCMHeader)
	binary.LittleEndian.PutUint16(out[10:12], seq)
	copy(out[12:], block)
	return out
}
