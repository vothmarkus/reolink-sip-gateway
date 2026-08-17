package sip

import (
	"net/url"
	"strings"
	"unicode"
)

// callerAllowlist matches the user part of a SIP/TEL identity. It deliberately
// does not infer country codes or use suffix matching: both would let a shorter
// number unintentionally authorize another caller. Formatting characters are
// ignored for dial strings, while named SIP users are compared case-insensitively.
type callerAllowlist struct {
	all bool
	ids map[string]struct{}
}

func newCallerAllowlist(values []string) callerAllowlist {
	result := callerAllowlist{ids: make(map[string]struct{}, len(values))}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "*" {
			result.all = true
			result.ids = nil
			return result
		}
		if id := canonicalCallerID(value); id != "" {
			result.ids[id] = struct{}{}
		}
	}
	return result
}

func (a callerAllowlist) allows(value string) bool {
	if a.all {
		return true
	}
	id := canonicalCallerID(value)
	if id == "" {
		return false
	}
	_, ok := a.ids[id]
	return ok
}

// CanonicalRemoteNumber returns the normalized SIP/TEL user identity used by
// both caller authorization and transient call-scoped integration events.
// It deliberately performs no country-code inference or suffix matching.
func CanonicalRemoteNumber(value string) string {
	return canonicalCallerID(value)
}

func canonicalCallerID(value string) string {
	value = strings.TrimSpace(extractURI(value))
	value = strings.Trim(value, "\"")
	lower := strings.ToLower(value)
	for _, scheme := range []string{"sip:", "sips:", "tel:"} {
		if strings.HasPrefix(lower, scheme) {
			value = value[len(scheme):]
			break
		}
	}
	if i := strings.LastIndexByte(value, '@'); i >= 0 {
		value = value[:i]
	}
	if i := strings.IndexAny(value, ";?"); i >= 0 {
		value = value[:i]
	}
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	value = strings.TrimSpace(strings.Trim(value, "\""))
	if value == "" {
		return ""
	}

	var dial strings.Builder
	dial.Grow(len(value))
	dialLike := true
	for index, r := range value {
		switch {
		case unicode.IsDigit(r), r == '*', r == '#':
			dial.WriteRune(r)
		case r == '+' && index == 0:
			dial.WriteRune(r)
		case unicode.IsSpace(r), r == '-', r == '(', r == ')', r == '.', r == '/':
			// Harmless presentation characters in telephone numbers.
		default:
			dialLike = false
		}
	}
	if dialLike && dial.Len() > 0 {
		return dial.String()
	}
	return strings.ToLower(value)
}
