package sip

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
)

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// parseAuthParams 解析 "Digest key="value", key=value" 形式的参数
func parseAuthParams(h string) map[string]string {
	params := map[string]string{}
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, ' '); i > 0 && strings.EqualFold(h[:i], "digest") {
		h = h[i+1:]
	}

	var key, val strings.Builder
	inKey := true
	inQuote := false

	flush := func() {
		k := strings.ToLower(strings.TrimSpace(key.String()))
		if k != "" {
			params[k] = strings.TrimSpace(val.String())
		}
		key.Reset()
		val.Reset()
		inKey = true
	}

	for _, r := range h {
		switch {
		case inKey && r == '=':
			inKey = false
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			flush()
		default:
			if inKey {
				key.WriteRune(r)
			} else {
				val.WriteRune(r)
			}
		}
	}
	flush()
	return params
}

// ParseChallenge 从 WWW-Authenticate 头中提取 realm、nonce 与 qop
func ParseChallenge(h string) (realm, nonce, qop string) {
	p := parseAuthParams(h)
	return p["realm"], p["nonce"], p["qop"]
}

// CheckDigest 校验 Authorization 头（RFC 2069 / RFC 2617 qop=auth）
func CheckDigest(authHeader, method, password string) bool {
	p := parseAuthParams(authHeader)
	if p["username"] == "" || p["response"] == "" || p["nonce"] == "" {
		return false
	}
	ha1 := md5hex(p["username"] + ":" + p["realm"] + ":" + password)
	ha2 := md5hex(method + ":" + p["uri"])

	var expect string
	if qop := p["qop"]; qop != "" {
		expect = md5hex(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":" + qop + ":" + ha2)
	} else {
		expect = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
	}
	return strings.EqualFold(expect, p["response"])
}

// BuildDigest 构造 Authorization 头值；useQop 为 true 时使用 qop=auth，否则使用 RFC 2069
func BuildDigest(username, realm, nonce, uri, method, password string, useQop bool) string {
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	if !useQop {
		response := md5hex(ha1 + ":" + nonce + ":" + ha2)
		return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5`,
			username, realm, nonce, uri, response)
	}
	nc := "00000001"
	cnonce := RandomHex(8)
	response := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5, qop=auth, nc=%s, cnonce="%s"`,
		username, realm, nonce, uri, response, nc, cnonce)
}
