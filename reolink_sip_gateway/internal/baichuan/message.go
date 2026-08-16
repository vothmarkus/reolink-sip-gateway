// Portions adapted from Shareed2k/reolinkproxy (MIT License).
// Copyright (c) 2026 Roman Kredentser. See THIRD-PARTY-NOTICES.md.
package baichuan

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

type request struct {
	MsgID      uint32
	ChannelID  uint8
	StreamType uint8
	Class      uint16
	MsgNum     uint16
	Extension  []byte
	Body       []byte
	Binary     bool
	ForceBC    bool
}
type pendingKey struct {
	msgID  uint32
	msgNum uint16
}
type closeState struct {
	err error
	mu  sync.Mutex
}

func (s *closeState) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}
func (s *closeState) get() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func (c *Client) readMessage() (*Message, error) {
	headerBuf := make([]byte, 20)
	if _, err := io.ReadFull(c.transport, headerBuf); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(headerBuf[0:4]) != magicHeader {
		return nil, fmt.Errorf("unexpected baichuan magic %#x", binary.LittleEndian.Uint32(headerBuf[0:4]))
	}
	h := Header{
		MsgID: binary.LittleEndian.Uint32(headerBuf[4:8]), BodyLen: binary.LittleEndian.Uint32(headerBuf[8:12]),
		ChannelID: headerBuf[12], StreamType: headerBuf[13], MsgNum: binary.LittleEndian.Uint16(headerBuf[14:16]),
		ResponseCode: binary.LittleEndian.Uint16(headerBuf[16:18]), Class: binary.LittleEndian.Uint16(headerBuf[18:20]),
	}
	if h.HasPayloadOffset() {
		off := make([]byte, 4)
		if _, err := io.ReadFull(c.transport, off); err != nil {
			return nil, err
		}
		h.PayloadOffset = binary.LittleEndian.Uint32(off)
	}
	if h.BodyLen > 8*1024*1024 {
		return nil, fmt.Errorf("implausibly large baichuan body length: %d", h.BodyLen)
	}
	body := make([]byte, h.BodyLen)
	if _, err := io.ReadFull(c.transport, body); err != nil {
		return nil, err
	}

	if h.MsgID == msgIDLogin && h.IsModern() && (h.ResponseCode>>8) == 0xDD {
		c.setNegotiatedEncryption(h.ResponseCode)
	}
	mode, aesKey, hasAESKey := c.snapshotCipher()
	extLen := uint32(0)
	if h.HasPayloadOffset() && h.PayloadOffset > 0 {
		extLen = h.PayloadOffset
	}
	if extLen > h.BodyLen {
		return nil, fmt.Errorf("invalid payload offset %d for body size %d", extLen, h.BodyLen)
	}
	extEncrypted, payloadEncrypted := body[:extLen], body[extLen:]
	var extension []byte
	prePayloadXML := ""
	if len(extEncrypted) > 0 {
		extension = decryptXML(h.ChannelID, extEncrypted, mode, aesKey, hasAESKey)
		prePayloadXML = trimXML(extension)
	}
	extensionMeta, _ := parseExtension(extension)
	binaryPayload := false
	if extensionMeta != nil && extensionMeta.BinaryData != nil && *extensionMeta.BinaryData == 1 {
		c.binaryMu.Lock()
		c.binaryMsgNums[h.MsgNum] = struct{}{}
		c.binaryMu.Unlock()
		binaryPayload = true
	} else {
		c.binaryMu.RLock()
		_, binaryPayload = c.binaryMsgNums[h.MsgNum]
		c.binaryMu.RUnlock()
	}
	var payload []byte
	xmlText := ""
	if len(payloadEncrypted) > 0 {
		if binaryPayload {
			payload = append([]byte(nil), payloadEncrypted...)
			encryptLen := 0
			if extensionMeta != nil && extensionMeta.EncryptLen != nil {
				encryptLen = *extensionMeta.EncryptLen
			} else if v, ok := parseEncryptLen(prePayloadXML); ok {
				encryptLen = v
			}
			if encryptLen > 0 && hasAESKey && encryptLen <= len(payloadEncrypted) {
				prefix := aesCFB(payloadEncrypted[:encryptLen], aesKey, false)
				payload = append(append([]byte(nil), prefix...), payloadEncrypted[encryptLen:]...)
			}
			xmlText = prePayloadXML
		} else {
			payload = decryptXML(h.ChannelID, payloadEncrypted, mode, aesKey, hasAESKey)
			xmlText = trimXML(payload)
		}
	}
	return &Message{Header: h, Extension: extension, Payload: payload, XML: xmlText, Binary: binaryPayload, ExtensionMeta: extensionMeta}, nil
}

func (c *Client) encodeRequest(req request) []byte {
	mode, aesKey, hasAESKey := c.snapshotCipher()
	if req.ForceBC {
		mode = EncryptionBC
	}
	extension := append([]byte(nil), req.Extension...)
	body := append([]byte(nil), req.Body...)
	if req.Class != classLegacy {
		if len(extension) > 0 {
			extension = encryptXML(req.ChannelID, extension, mode, aesKey, hasAESKey)
		}
		if !req.Binary && len(body) > 0 {
			body = encryptXML(req.ChannelID, body, mode, aesKey, hasAESKey)
		}
	}
	headerLen := 20
	if req.Class == classModernWithOffset || req.Class == 0 {
		headerLen = 24
	}
	packet := make([]byte, headerLen+len(extension)+len(body))
	binary.LittleEndian.PutUint32(packet[0:4], magicHeader)
	binary.LittleEndian.PutUint32(packet[4:8], req.MsgID)
	binary.LittleEndian.PutUint32(packet[8:12], uint32(len(extension)+len(body))) // #nosec G115
	packet[12], packet[13] = req.ChannelID, req.StreamType
	binary.LittleEndian.PutUint16(packet[14:16], req.MsgNum)
	responseCode := uint16(0)
	if req.Class == classLegacy && req.MsgID == msgIDLogin && len(body) == 0 {
		responseCode = 0xDC12
	}
	binary.LittleEndian.PutUint16(packet[16:18], responseCode)
	binary.LittleEndian.PutUint16(packet[18:20], req.Class)
	if headerLen == 24 {
		off := uint32(0)
		if len(extension) > 0 {
			off = uint32(len(extension))
		}
		binary.LittleEndian.PutUint32(packet[20:24], off)
	}
	copy(packet[headerLen:], extension)
	copy(packet[headerLen+len(extension):], body)
	return packet
}
