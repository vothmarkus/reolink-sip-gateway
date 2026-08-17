package sip

import "testing"

func TestCanonicalCallerID(t *testing.T) {
	tests := map[string]string{
		`"Example" <sip:0123-456 789@fritz.box>;tag=abc`: "0123456789",
		`sip:%2B49%20123%20456789@fritz.box`:             "+49123456789",
		`tel:+49 (123) 456789;phone-context=home`:        "+49123456789",
		`sip:**620@fritz.box`:                            "**620",
		`sip:FrontDoor@fritz.box`:                        "frontdoor",
	}
	for input, want := range tests {
		if got := canonicalCallerID(input); got != want {
			t.Fatalf("canonicalCallerID(%q)=%q want %q", input, got, want)
		}
	}
}

func TestCanonicalRemoteNumberUsesTheAuthorizationIdentity(t *testing.T) {
	input := `"Example" <sip:%2B49 (123) 456789@fritz.box>;tag=abc`
	if got, want := CanonicalRemoteNumber(input), "+49123456789"; got != want {
		t.Fatalf("CanonicalRemoteNumber(%q)=%q want %q", input, got, want)
	}
}

func TestCallerAllowlistMatchesExactlyAfterFormattingNormalization(t *testing.T) {
	allowed := newCallerAllowlist([]string{"0123 456789", "**620", "FrontDoor"})
	for _, caller := range []string{
		`"Example" <sip:0123-456789@fritz.box>;tag=abc`,
		`sip:**620@fritz.box`,
		`sip:frontdoor@fritz.box`,
	} {
		if !allowed.allows(caller) {
			t.Fatalf("expected %q to be allowed", caller)
		}
	}
	for _, caller := range []string{
		`sip:+49123456789@fritz.box`, // No implicit country-code conversion.
		`sip:456789@fritz.box`,       // No unsafe suffix matching.
		`sip:anonymous@anonymous.invalid`,
	} {
		if allowed.allows(caller) {
			t.Fatalf("expected %q to be rejected", caller)
		}
	}
}

func TestCallerAllowlistWildcardAndEmptyFailClosed(t *testing.T) {
	if !newCallerAllowlist([]string{"*"}).allows("sip:anyone@example.invalid") {
		t.Fatal("wildcard should allow every non-empty caller")
	}
	if newCallerAllowlist(nil).allows("sip:123@example.invalid") {
		t.Fatal("empty whitelist must reject callers")
	}
}
