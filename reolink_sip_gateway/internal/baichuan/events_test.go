package baichuan

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Exercise actual TCP framing, host-channel XOR login, ACK-free event pushes,
// and cleanup after cancellation. A live camera is deliberately not required.
func TestEventConnectionHandshakeVariants(t *testing.T) {
	for _, scenario := range []string{"ack", "push_without_ack", "bad_nonce", "denied_login", "denied_subscription", "silent", "invalid_push"} {
		t.Run(scenario, func(t *testing.T) {
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			serverDone := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				serverDone <- eventServerScenario(conn, scenario)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := Dial(ctx, Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Username: "admin", Password: "secret", ControlChannel: 250})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var stages []string
			err = client.LoginWithProgress(ctx, func(s string) { stages = append(stages, s) })
			wantStages := []string{"nonce", "login"}
			if scenario == "bad_nonce" {
				wantStages = []string{"nonce"}
			}
			if !reflect.DeepEqual(stages, wantStages) {
				t.Fatalf("stages: %v", stages)
			}
			if scenario == "bad_nonce" {
				if err == nil || !strings.Contains(err.Error(), "invalid nonce response") || strings.Contains(err.Error(), "private-auth-hash") {
					t.Fatalf("nonce error leaks response or was ignored: %v", err)
				}
			} else if scenario == "denied_login" {
				var status *StatusError
				if !errors.As(err, &status) || status.Code != 401 {
					t.Fatalf("login denial lost: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				subCtx := ctx
				if scenario == "silent" || scenario == "invalid_push" {
					var stop context.CancelFunc
					subCtx, stop = context.WithTimeout(ctx, 150*time.Millisecond)
					defer stop()
				}
				events, unsubscribe, err := client.SubscribeEvents(subCtx)
				switch scenario {
				case "silent", "invalid_push":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("expected bounded subscription timeout: %v", err)
					}
				case "denied_subscription":
					var status *StatusError
					if !errors.As(err, &status) || status.Code != 403 {
						t.Fatalf("subscription denial lost: %v", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
					defer unsubscribe()
					if scenario == "push_without_ack" {
						for i, want := range []bool{false, true} {
							select {
							case message := <-events:
								alarms, err := ParseAlarmEvents(message.XML)
								if err != nil || len(alarms) != 1 || alarms[0].Visitor != want {
									t.Fatalf("push %d lost/reordered: %v %v", i, alarms, err)
								}
							case <-ctx.Done():
								t.Fatal("push dropped")
							}
						}
					}
					if err := client.CheckEventConnection(ctx); err != nil {
						t.Fatal("host keepalive failed:", err)
					}
				}
			}
			client.Close()
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func eventServerScenario(conn net.Conn, scenario string) error {
	nonce := "event-nonce"
	key := DeriveAESKey(nonce, "secret")
	for step := 0; step < 3; step++ {
		req, err := readTestRequest(conn)
		if err != nil {
			return err
		}
		channel := uint8(250)
		if step == 2 {
			channel = 251
		}
		id := uint32(1)
		if step == 2 {
			id = 31
		}
		if req.Header.ChannelID != channel || req.Header.MsgID != id {
			return fmt.Errorf("step %d header: %+v", step, req.Header)
		}
		header := Header{MsgID: id, ChannelID: channel, MsgNum: req.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}
		switch step {
		case 0:
			header.Class, header.ResponseCode = classModern, 0xDD02
			if scenario == "bad_nonce" {
				return writeTestResponse(conn, header, nil, []byte(`<body><Unexpected>private-auth-hash</Unexpected></body>`), EncryptionBC, [16]byte{}, false)
			}
			err = writeTestResponse(conn, header, nil, []byte(`<body><Encryption><nonce>`+nonce+`</nonce></Encryption></body>`), EncryptionBC, [16]byte{}, false)
		case 1:
			xml := string(BCXOR(channel, req.Body))
			if !strings.Contains(xml, MD5Modern("admin"+nonce)) || !strings.Contains(xml, MD5Modern("secret"+nonce)) {
				return fmt.Errorf("invalid host-channel login encryption")
			}
			if scenario == "denied_login" {
				header.ResponseCode = 401
			}
			err = writeTestResponse(conn, header, nil, nil, EncryptionAES, key, true)
			if scenario == "denied_login" {
				return err
			}
		case 2:
			switch scenario {
			case "ack", "denied_subscription":
				if scenario == "denied_subscription" {
					header.ResponseCode = 403
				}
				err = writeTestResponse(conn, header, nil, nil, EncryptionAES, key, true)
				if scenario == "denied_subscription" {
					return err
				}
			case "push_without_ack", "invalid_push":
				for i, status := range []string{"none", "visitor"} {
					xml := `<body><AlarmEventList><AlarmEvent><channelId>0</channelId><status>` + status + `</status></AlarmEvent></AlarmEventList></body>`
					if scenario == "invalid_push" {
						xml = `<body><AlarmEvent>`
					}
					if err = writeTestResponse(conn, Header{MsgID: 33, MsgNum: uint16(500 + i), ResponseCode: 200, Class: classModernWithOffset}, nil, []byte(xml), EncryptionAES, key, true); err != nil {
						return err
					}
				}
			}
		}
		if err != nil {
			return err
		}
	}
	if scenario == "silent" || scenario == "invalid_push" {
		_, err := readTestRequest(conn)
		if err == io.EOF {
			return nil
		}
		return fmt.Errorf("expected closed subscription, got %v", err)
	}
	ping, err := readTestRequest(conn)
	if err != nil {
		return err
	}
	if ping.Header.MsgID != 93 || ping.Header.ChannelID != 250 {
		return fmt.Errorf("wrong event keepalive: %+v", ping.Header)
	}
	return writeTestResponse(conn, Header{MsgID: 93, ChannelID: 250, MsgNum: ping.Header.MsgNum, ResponseCode: 200, Class: classModernWithOffset}, nil, nil, EncryptionAES, key, true)
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
