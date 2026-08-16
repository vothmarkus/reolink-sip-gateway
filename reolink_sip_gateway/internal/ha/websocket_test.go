package ha

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestReadFrameRejectsOversizeBeforePayloadRead(t *testing.T) {
	var frame bytes.Buffer
	frame.WriteByte(0x80 | wsOpcodeText)
	frame.WriteByte(127)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(wsMaxMessageSize+1))
	frame.Write(length[:])

	conn := &wsConn{reader: bufio.NewReader(&frame)}
	_, _, _, err := conn.readFrame()
	if err == nil || !strings.Contains(err.Error(), "websocket frame too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}
