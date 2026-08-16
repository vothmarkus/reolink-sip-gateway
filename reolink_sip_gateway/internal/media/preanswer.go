package media

import (
	"errors"
	"net"
	"time"
)

const (
	preAnswerDrainIdle  = 2 * time.Millisecond
	preAnswerDrainLimit = 4096
)

// discardQueuedSIPRTP removes datagrams that accumulated on the reserved SIP
// RTP socket before the live media workers take ownership of it. This happens
// with PBXs that send early media during 183 Session Progress. The function
// stops as soon as the receive queue is idle for a very short interval, so it
// does not intentionally consume the subsequent live stream. The hard packet
// limit prevents a hostile or broken peer from trapping media startup in an
// unbounded drain loop.
func discardQueuedSIPRTP(conn *net.UDPConn) (discarded int, limitReached bool, err error) {
	if conn == nil {
		return 0, false, errors.New("nil SIP RTP socket")
	}
	defer func() {
		// The live RTP receiver manages its own deadlines. Do not leak the drain
		// deadline into it.
		if resetErr := conn.SetReadDeadline(time.Time{}); err == nil && resetErr != nil {
			err = resetErr
		}
	}()

	buf := make([]byte, 4096)
	for discarded < preAnswerDrainLimit {
		if err = conn.SetReadDeadline(time.Now().Add(preAnswerDrainIdle)); err != nil {
			return discarded, false, err
		}
		_, _, readErr := conn.ReadFromUDP(buf)
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				return discarded, false, nil
			}
			return discarded, false, readErr
		}
		discarded++
	}
	return discarded, true, nil
}
