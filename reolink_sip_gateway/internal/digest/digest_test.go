package digest

import (
	"strings"
	"testing"
)

func TestRFC2617NoQOP(t *testing.T) {
	c := Challenge{Scheme: "digest", Realm: "testrealm@host.com", Nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093", Algorithm: "MD5"}
	a := Authorization(c, "Mufasa", "Circle Of Life", "GET", "/dir/index.html", 1)
	if !strings.Contains(a, `response="670fd8c2df070c60b045671b8b24ff02"`) {
		t.Fatalf("unexpected auth: %s", a)
	}
}

func TestParse(t *testing.T) {
	c, err := Parse(`Digest realm="fritz.box", nonce="abc", qop="auth", opaque="x"`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Realm != "fritz.box" || c.QOP != "auth" || c.Opaque != "x" {
		t.Fatalf("bad challenge: %#v", c)
	}
}

func TestRejectUnsupportedQOP(t *testing.T) {
	if _, err := Parse(`Digest realm="x", nonce="y", qop="auth-int"`); err == nil {
		t.Fatal("expected unsupported qop to be rejected")
	}
}

func TestAuthorizationOmitsImplicitMD5Algorithm(t *testing.T) {
	c, err := Parse(`Digest realm="cam", nonce="abc", qop="auth"`)
	if err != nil {
		t.Fatal(err)
	}
	a := Authorization(c, "user", "pass", "DESCRIBE", "rtsp://cam/live", 1)
	if strings.Contains(strings.ToLower(a), "algorithm=") {
		t.Fatalf("implicit MD5 algorithm must not be echoed: %s", a)
	}
}

func TestAuthorizationEchoesExplicitMD5Algorithm(t *testing.T) {
	c, err := Parse(`Digest realm="cam", nonce="abc", algorithm=MD5, qop="auth"`)
	if err != nil {
		t.Fatal(err)
	}
	a := Authorization(c, "user", "pass", "DESCRIBE", "rtsp://cam/live", 1)
	if !strings.Contains(a, "algorithm=MD5") {
		t.Fatalf("explicit MD5 algorithm missing: %s", a)
	}
}

func TestParseSHA256(t *testing.T) {
	c, err := Parse(`Digest realm="cam", nonce="abc", algorithm=SHA-256, qop="auth"`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Algorithm != "SHA-256" || !c.AlgorithmExplicit {
		t.Fatalf("bad challenge: %#v", c)
	}
	a := Authorization(c, "user", "pass", "DESCRIBE", "rtsp://cam/live", 1)
	if !strings.Contains(a, "algorithm=SHA-256") {
		t.Fatalf("SHA-256 algorithm missing: %s", a)
	}
}
