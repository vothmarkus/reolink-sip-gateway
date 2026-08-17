package media

import (
	"errors"
	"fmt"
	"time"
)

var ErrRTPInactivity = errors.New("SIP RTP inactivity watchdog expired")

type rtpWatchdog struct {
	timeout time.Duration
	last    time.Time
}

func newRTPWatchdog(timeout time.Duration, now time.Time) *rtpWatchdog {
	return &rtpWatchdog{timeout: timeout, last: now}
}

func (w *rtpWatchdog) Mark(now time.Time) {
	if w != nil {
		w.last = now
	}
}

func (w *rtpWatchdog) Check(now time.Time) error {
	if w == nil || w.timeout <= 0 || w.last.IsZero() || now.Sub(w.last) < w.timeout {
		return nil
	}
	return fmt.Errorf("%w after %s without valid negotiated audio", ErrRTPInactivity, w.timeout)
}
