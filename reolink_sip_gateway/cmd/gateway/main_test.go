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
	if err := commands.StartTestCall(context.Background(), "wohnung_1"); !errors.Is(err, statuspkg.ErrCommandUnavailable) {
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
		func(_ context.Context, routeID string) error {
			if routeID != "wohnung_1" {
				t.Fatalf("route ID=%q", routeID)
			}
			testCalls++
			return nil
		},
		func(context.Context) error { hangups++; return nil },
	)
	if err := commands.StartTestCall(context.Background(), "wohnung_1"); err != nil {
		t.Fatal(err)
	}
	if err := commands.Hangup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if testCalls != 1 || hangups != 1 {
		t.Fatalf("callbacks test=%d hangup=%d", testCalls, hangups)
	}
	commands.Disable()
	if err := commands.StartTestCall(context.Background(), "wohnung_1"); !errors.Is(err, statuspkg.ErrCommandUnavailable) {
		t.Fatalf("disabled command error = %v", err)
	}
}

func TestBothSIPAccountConfigsAcceptIncomingCalls(t *testing.T) {
	cfg := config.Defaults()
	cfg.IncomingCallsEnabled = true
	cfg.IncomingAllowedCallers = []string{"**620", "0163"}
	cfg.CallRoutes[0].MobileNumber1 = "0163"
	cfg.CallRoutes[0].MobileNumber2 = "0176"
	cfg.CallRoutes[0].MobileNumber3 = "0151"

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

func TestRouteHelpersPreserveStableIDsAndDefaultRoute(t *testing.T) {
	cfg := config.Defaults()
	routes := cfg.ResolvedCallRoutes()
	if len(routes) != 1 || routes[0].ID != config.DefaultRouteID {
		t.Fatalf("default routes=%#v", routes)
	}
	if route, ok := requestedCallRoute(routes, ""); !ok || route.ID != config.DefaultRouteID {
		t.Fatalf("default route=%#v ok=%t", route, ok)
	}

	routes = []config.ResolvedCallRoute{
		{ID: "wohnung_1", Name: "Wohnung 1", VisitorEntity: "binary_sensor.one", DoorbellNumber: "11"},
		{ID: "wohnung_2", Name: "Wohnung 2", VisitorEntity: "binary_sensor.two", MobileTargets: []config.MobileTarget{{ID: "maria", Destination: "0176"}}},
	}
	subscriptions := routeSubscriptions(routes)
	if len(subscriptions) != 2 || subscriptions[1].RouteID != "wohnung_2" || subscriptions[1].EntityID != "binary_sensor.two" {
		t.Fatalf("subscriptions=%#v", subscriptions)
	}
	definitions := statusRouteDefinitions(config.Config{DoorCallEnabled: true, ParallelCallEnabled: true}, routes)
	if !definitions[0].DoorCall || definitions[0].MobileCall || definitions[1].DoorCall || !definitions[1].MobileCall {
		t.Fatalf("route definitions=%#v", definitions)
	}
	if _, ok := requestedCallRoute(routes, "missing"); ok {
		t.Fatal("unknown route unexpectedly resolved")
	}
}

func TestLocalIPv4ForRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := localIPv4ForRemote(ctx, "127.0.0.1", 5060)
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("local address=%q", got)
	}
}
