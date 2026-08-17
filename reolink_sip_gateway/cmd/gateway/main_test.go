package main

import (
	"context"
	"errors"
	"testing"

	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

func TestGatewayCommandsFailClosedUntilConfigured(t *testing.T) {
	commands := &gatewayCommands{}
	if err := commands.StartTestCall(context.Background()); !errors.Is(err, statuspkg.ErrCommandUnavailable) {
		t.Fatalf("test call error = %v", err)
	}
	if err := commands.Hangup(context.Background()); !errors.Is(err, statuspkg.ErrCommandUnavailable) {
		t.Fatalf("hangup error = %v", err)
	}
}

func TestGatewayCommandsConfigureAndDisable(t *testing.T) {
	commands := &gatewayCommands{}
	testCalls := 0
	hangups := 0
	commands.Configure(
		func(context.Context) error { testCalls++; return nil },
		func(context.Context) error { hangups++; return nil },
	)
	if err := commands.StartTestCall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := commands.Hangup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if testCalls != 1 || hangups != 1 {
		t.Fatalf("callbacks test=%d hangup=%d", testCalls, hangups)
	}
	commands.Disable()
	if err := commands.StartTestCall(context.Background()); !errors.Is(err, statuspkg.ErrCommandUnavailable) {
		t.Fatalf("disabled command error = %v", err)
	}
}
