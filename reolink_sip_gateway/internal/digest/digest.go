package digest

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type Challenge struct {
	Scheme            string
	Realm             string
	Nonce             string
	Opaque            string
	Algorithm         string
	AlgorithmExplicit bool
	QOP               string
	Stale             bool
}

func Parse(header string) (Challenge, error) {
	header = strings.TrimSpace(header)
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return Challenge{}, fmt.Errorf("invalid authentication challenge")
	}
	c := Challenge{Scheme: strings.ToLower(parts[0]), Algorithm: "MD5"}
	params := parseParams(parts[1])
	qopPresent := false
	for k, v := range params {
		switch strings.ToLower(k) {
		case "realm":
			c.Realm = v
		case "nonce":
			c.Nonce = v
		case "opaque":
			c.Opaque = v
		case "algorithm":
			c.Algorithm = v
			c.AlgorithmExplicit = true
		case "qop":
			qopPresent = true
			for _, q := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(q), "auth") {
					c.QOP = "auth"
					break
				}
			}
		case "stale":
			c.Stale = strings.EqualFold(v, "true")
		}
	}
	if c.Scheme != "digest" || c.Realm == "" || c.Nonce == "" {
		return Challenge{}, fmt.Errorf("unsupported or incomplete digest challenge")
	}
	if !supportedAlgorithm(c.Algorithm) {
		return Challenge{}, fmt.Errorf("unsupported digest algorithm %q", c.Algorithm)
	}
	if qopPresent && c.QOP == "" {
		return Challenge{}, fmt.Errorf("digest challenge does not offer supported qop=auth")
	}
	return c, nil
}

func Authorization(c Challenge, username, password, method, uri string, nc uint32) string {
	algorithm := normalizeAlgorithm(c.Algorithm)
	ha1 := hashHex(algorithm, username+":"+c.Realm+":"+password)
	ha2 := hashHex(algorithm, method+":"+uri)
	parts := []string{
		fmt.Sprintf("username=\"%s\"", escape(username)),
		fmt.Sprintf("realm=\"%s\"", escape(c.Realm)),
		fmt.Sprintf("nonce=\"%s\"", escape(c.Nonce)),
		fmt.Sprintf("uri=\"%s\"", escape(uri)),
	}
	if c.QOP == "auth" {
		cnonce := randomHex(8)
		ncString := fmt.Sprintf("%08x", nc)
		response := hashHex(algorithm, ha1+":"+c.Nonce+":"+ncString+":"+cnonce+":auth:"+ha2)
		parts = append(parts,
			fmt.Sprintf("response=\"%s\"", response),
			"qop=auth",
			"nc="+ncString,
			fmt.Sprintf("cnonce=\"%s\"", cnonce),
		)
	} else {
		response := hashHex(algorithm, ha1+":"+c.Nonce+":"+ha2)
		parts = append(parts, fmt.Sprintf("response=\"%s\"", response))
	}
	// RFC 2617/7616 define MD5 as the default when the server omits the
	// algorithm directive. Some embedded RTSP servers are strict about the
	// echoed directives, so only send algorithm when it was explicitly
	// challenged (or when a non-default algorithm is actually in use).
	if c.AlgorithmExplicit || !strings.EqualFold(algorithm, "MD5") {
		parts = append(parts, "algorithm="+algorithm)
	}
	if c.Opaque != "" {
		parts = append(parts, fmt.Sprintf("opaque=\"%s\"", escape(c.Opaque)))
	}
	return "Digest " + strings.Join(parts, ", ")
}

func supportedAlgorithm(algorithm string) bool {
	switch normalizeAlgorithm(algorithm) {
	case "MD5", "SHA-256":
		return true
	default:
		return false
	}
}

func normalizeAlgorithm(algorithm string) string {
	algorithm = strings.TrimSpace(algorithm)
	if algorithm == "" || strings.EqualFold(algorithm, "MD5") {
		return "MD5"
	}
	if strings.EqualFold(algorithm, "SHA-256") {
		return "SHA-256"
	}
	return algorithm
}

func hashHex(algorithm, s string) string {
	switch normalizeAlgorithm(algorithm) {
	case "SHA-256":
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	default:
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef"[:n*2]
	}
	return hex.EncodeToString(b)
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

func parseParams(s string) map[string]string {
	out := make(map[string]string)
	var key strings.Builder
	var val strings.Builder
	inQuote := false
	escaped := false
	readingValue := false
	flush := func() {
		k := strings.TrimSpace(key.String())
		v := strings.TrimSpace(val.String())
		v = strings.Trim(v, `"`)
		if k != "" {
			out[k] = v
		}
		key.Reset()
		val.Reset()
		readingValue = false
	}
	for _, r := range s {
		if escaped {
			if readingValue {
				val.WriteRune(r)
			} else {
				key.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			if readingValue {
				val.WriteRune(r)
			}
			continue
		}
		if !inQuote && r == '=' && !readingValue {
			readingValue = true
			continue
		}
		if !inQuote && r == ',' {
			flush()
			continue
		}
		if readingValue {
			val.WriteRune(r)
		} else {
			key.WriteRune(r)
		}
	}
	flush()
	return out
}
