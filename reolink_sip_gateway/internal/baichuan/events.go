package baichuan

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode"
)

const msgIDAlarmSubscribe = 31
const msgIDAlarmEvent = 33

// SubscribeEvents attaches the receiver before asking for push events, because
// a camera may send its initial state before acknowledging command 31.
// Protocol reference: reolink_aio/baichuan/baichuan.py, subscribe_events.
func (c *Client) SubscribeEvents(ctx context.Context) (<-chan *Message, func(), error) {
	if err := c.Login(ctx); err != nil {
		return nil, nil, err
	}
	events, unsubscribe := c.subscribe(msgIDAlarmEvent, 64, true)
	// Keep the consumer queue intact (and ordered) while independently watching
	// for an authenticated event push. Some cameras push before/without the
	// command-31 reply; waiting exclusively for the reply discards a working
	// event session on timeout. No fallback is allowed before successful login.
	witness, stopWitness := c.subscribe(msgIDAlarmEvent, 8, false)
	defer stopWitness()
	requestCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	var requestErr error
	go func() { requestErr = c.RenewEvents(requestCtx); close(finished) }()
	defer func() { cancel(); <-finished }()
	for {
		select {
		case <-finished:
			if requestErr == nil {
				return events, unsubscribe, nil
			}
			unsubscribe()
			return nil, nil, requestErr
		case msg := <-witness:
			alarms, err := ParseAlarmEvents(msg.XML)
			if err == nil && len(alarms) > 0 {
				if err := ctx.Err(); err != nil {
					unsubscribe()
					return nil, nil, err
				}
				return events, unsubscribe, nil
			}
		case <-ctx.Done():
			unsubscribe()
			return nil, nil, ctx.Err()
		case <-c.Done():
			// A camera may close immediately after rejecting command 31. Let
			// roundTripRequest consume that buffered response before using EOF,
			// otherwise the useful rejection code disappears from diagnostics.
			<-finished
			unsubscribe()
			if requestErr != nil {
				return nil, nil, requestErr
			}
			return nil, nil, c.Err()
		}
	}
}

// RenewEvents doubles as a bounded request/response liveness check. Repeated
// subscriptions are supported by Reolink and restore a silently lost subscription.
func (c *Client) RenewEvents(ctx context.Context) error {
	_, err := c.sendRequest(ctx, request{MsgID: msgIDAlarmSubscribe, ChannelID: 251, Class: classModernWithOffset})
	return err
}

// CheckEventConnection is a bounded liveness check after receiving events.
// Re-subscribing every 30 seconds needlessly replays camera state snapshots.
func (c *Client) CheckEventConnection(ctx context.Context) error {
	_, err := c.sendRequest(ctx, request{MsgID: msgIDPing, ChannelID: c.cfg.ControlChannel, Class: classModernWithOffset})
	return err
}

type AlarmEvent struct {
	Channel int
	Visitor bool
}

// ParseAlarmEvents ignores motion/AI-only messages and refuses to guess a channel.
// Reolink XML channelId is zero-based, including on an NVR.
func ParseAlarmEvents(body string) ([]AlarmEvent, error) {
	if len(body) > 128*1024 {
		return nil, errors.New("alarm XML exceeds 128 KiB")
	}
	decoder := xml.NewDecoder(strings.NewReader(body))
	var events []AlarmEvent
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			return nil, errors.New("invalid alarm XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "AlarmEvent" {
			continue
		}
		var alarm struct {
			Channel *int    `xml:"channelId"`
			Status  *string `xml:"status"`
		}
		if err := decoder.DecodeElement(&alarm, &start); err != nil {
			return nil, errors.New("invalid alarm event")
		}
		if alarm.Channel == nil || *alarm.Channel < 0 || *alarm.Channel > 255 || alarm.Status == nil {
			continue
		}
		visitor := false
		for _, state := range strings.FieldsFunc(*alarm.Status, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;|", r) }) {
			visitor = visitor || strings.EqualFold(state, "visitor")
		}
		events = append(events, AlarmEvent{Channel: *alarm.Channel, Visitor: visitor})
	}
}
