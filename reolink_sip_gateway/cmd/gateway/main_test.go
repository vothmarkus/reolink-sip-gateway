package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
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

func TestBothSIPAccountConfigsAcceptIncomingCalls(t *testing.T) {
	cfg := config.Defaults()
	cfg.IncomingCallsEnabled = true
	cfg.IncomingAllowedCallers = []string{"**620", "0163"}
	cfg.ParallelDestinations = []string{"0163", "0176", "0151"}

	door := doorSIPConfig(cfg)
	mobile := parallelSIPConfig(cfg)
	for name, account := range map[string]sip.Config{"door": door, "mobile": mobile} {
		if !account.AcceptIncoming {
			t.Fatalf("%s account does not accept incoming calls", name)
		}
		if len(account.AllowedCallers) != 2 || account.AllowedCallers[0] != "**620" {
			t.Fatalf("%s account has wrong caller policy: %#v", name, account.AllowedCallers)
		}
	}
	if door.MaxConcurrentCalls != 1 || mobile.MaxConcurrentCalls != 3 {
		t.Fatalf("unexpected account capacities: door=%d mobile=%d", door.MaxConcurrentCalls, mobile.MaxConcurrentCalls)
	}
}

func TestMergeIncomingInviteChannelsForwardsBothAccounts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	door := make(chan *sip.IncomingInvite, 1)
	mobile := make(chan *sip.IncomingInvite, 1)
	doorInvite := &sip.IncomingInvite{}
	mobileInvite := &sip.IncomingInvite{}
	door <- doorInvite
	mobile <- mobileInvite
	merged := mergeIncomingInviteChannels(ctx, door, mobile)

	seen := make(map[*sip.IncomingInvite]bool, 2)
	for range 2 {
		select {
		case invite := <-merged:
			seen[invite] = true
		case <-time.After(time.Second):
			t.Fatal("incoming account channel was not forwarded")
		}
	}
	if !seen[doorInvite] || !seen[mobileInvite] {
		t.Fatalf("merged incoming calls=%#v", seen)
	}
}

func TestConfiguredSIPAccountCountSupportsMobileOnly(t *testing.T) {
	cfg := config.Defaults()
	if got := configuredSIPAccountCount(cfg); got != 1 {
		t.Fatalf("default account count=%d", got)
	}
	cfg.DoorCallEnabled = false
	cfg.ParallelCallEnabled = true
	if got := configuredSIPAccountCount(cfg); got != 1 {
		t.Fatalf("mobile-only account count=%d", got)
	}
	cfg.DoorCallEnabled = true
	if got := configuredSIPAccountCount(cfg); got != 2 {
		t.Fatalf("dual-account count=%d", got)
	}
}
