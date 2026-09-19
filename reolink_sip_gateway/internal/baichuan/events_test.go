package baichuan

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseVisitorEventsSeparatesChannelsAndIgnoresOtherEvents(t *testing.T) {
	xml := `<body><AlarmEventList><AlarmEvent><channelId>0</channelId><status>MD,visitor</status></AlarmEvent><AlarmEvent><channelId>7</channelId><status>none</status></AlarmEvent><AlarmEvent><channelId>8</channelId><AItype>people</AItype></AlarmEvent><AlarmEvent><status>visitor</status></AlarmEvent><AlarmEvent><channelId>256</channelId><status>visitor</status></AlarmEvent><DayNightEvent><channelId>4</channelId><status>visitor</status></DayNightEvent><AlarmEvent><channelId>5</channelId><status>notvisitor</status></AlarmEvent></AlarmEventList></body>`
	got, err := ParseAlarmEvents(xml)
	want := []AlarmEvent{{Channel: 0, Visitor: true}, {Channel: 7, Visitor: false}, {Channel: 5, Visitor: false}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%+v, err=%v", got, err)
	}
	for _, invalid := range []string{"<body><AlarmEvent>", strings.Repeat("x", 128*1024+1)} {
		if _, err := ParseAlarmEvents(invalid); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
}

func TestEventSubscriptionReceivesPushBeforeAcknowledgment(t *testing.T) {
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
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		key := DeriveAESKey("event-nonce", "secret")
		for step := 0; step < 4; step++ {
			req, err := readTestRequest(conn)
			if err != nil {
				serverErr <- err
				return
			}
			if step == 0 {
				err = writeTestResponse(conn, Header{MsgID: 1, MsgNum: req.Header.MsgNum, ResponseCode: 0xDD02, Class: classModern}, nil, []byte(`<body><Encryption><nonce>event-nonce</nonce></Encryption></body>`), EncryptionBC, [16]byte{}, false)
			} else if step == 1 {
				err = writeTestResponse(conn, Header{MsgID: 1, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, key, true)
			} else {
				if req.Header.MsgID != 31 || req.Header.ChannelID != 251 {
					serverErr <- fmt.Errorf("unexpected subscription: %+v", req.Header)
					return
				}
				if step == 2 {
					err = writeTestResponse(conn, Header{MsgID: 33, MsgNum: 500, ResponseCode: 200, Class: classModernWithOffset}, nil, []byte(`<body><AlarmEventList><AlarmEvent><channelId>1</channelId><status>visitor</status></AlarmEvent></AlarmEventList></body>`), EncryptionAES, key, true)
					if err != nil {
						serverErr <- err
						return
					}
				}
				err = writeTestResponse(conn, Header{MsgID: 31, ChannelID: 251, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, key, true)
			}
			if err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, err := Dial(ctx, Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Username: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	events, unsubscribe, err := client.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case event := <-events:
		alarms, err := ParseAlarmEvents(event.XML)
		if err != nil || len(alarms) != 1 || alarms[0].Channel != 1 || !alarms[0].Visitor {
			t.Fatalf("alarms=%+v, err=%v", alarms, err)
		}
	case <-ctx.Done():
		t.Fatal("missed initial push")
	}
	if err := client.RenewEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}
