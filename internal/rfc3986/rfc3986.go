// Package rfc3986 normalises a URI the way RFC 3986 §6.2.2 and §6.2.3 say,
// for comparing two spellings of one request URI.
package rfc3986

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// URI normalisation for comparing a DPoP proof's htu with the request it
// came with. RFC 9449 §4.3 compares the two "after applying syntax-based
// normalization as defined in Section 6.2.2 of [RFC3986] and scheme-based
// normalization as defined in Section 6.2.3 of [RFC3986]", with no query and
// no fragment. net/url parses; it does not normalise, so the steps the RFC
// names are applied here, each to the component it concerns.

// defaultPorts are the ports scheme-based normalisation drops (RFC 3986
// §6.2.3): the scheme's own, which is the same as none.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// errNotAbsolute is a URI with no scheme or no host, which names no request.
var errNotAbsolute = errors.New("not an absolute URI")

// errUserinfo is a URI with userinfo, which an http(s) URI must not carry.
var errUserinfo = errors.New("userinfo in an http(s) URI")

// Normalize is raw as RFC 9449 compares it: scheme and host in lower case,
// the scheme's default port dropped, an empty path as "/", percent-encodings
// of unreserved characters decoded and every other one in upper case, dot
// segments removed — and no query or fragment.
func Normalize(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Hostname() == "" {
		return "", fmt.Errorf("%q: %w", raw, errNotAbsolute)
	}
	// RFC 9110 §4.2.4: an http(s) URI carries no userinfo.
	if u.User != nil {
		return "", fmt.Errorf("%q: %w", raw, errUserinfo)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	// A host with a colon is an IPv6 literal, or it is no host at all.
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return "", fmt.Errorf("%q: host: %w", raw, err)
		}
	}
	if port := u.Port(); port != "" && port != defaultPorts[scheme] {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 literal keeps its brackets
	}
	return scheme + "://" + host + NormalizePath(u.EscapedPath()), nil
}

// NormalizePath is an escaped path with its percent-encodings normalised
// (RFC 3986 §6.2.2.1, §6.2.2.2), its dot segments removed (§6.2.2.3) and, if
// empty, as "/" (§6.2.3).
func NormalizePath(escaped string) string {
	p := removeDotSegments(normalizePercentEncoding(escaped))
	if p == "" {
		return "/"
	}
	return p
}

// normalizePercentEncoding decodes each percent-encoded unreserved character
// (RFC 3986 §2.3) and writes every other percent-encoding with upper-case
// hex digits. A malformed escape is kept as it is.
func normalizePercentEncoding(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		hi, okHi := unhex(s[i+1])
		lo, okLo := unhex(s[i+2])
		if !okHi || !okLo {
			b.WriteByte(s[i])
			continue
		}
		c := hi<<4 | lo
		if IsUnreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteString(strings.ToUpper(s[i : i+3]))
		}
		i += 2
	}
	return b.String()
}

// IsUnreserved reports whether c is an RFC 3986 §2.3 unreserved character.
func IsUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	}
	return c == '-' || c == '.' || c == '_' || c == '~'
}

// unhex is the value of a hex digit, and whether c is one.
func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// removeDotSegments is RFC 3986 §5.2.4's algorithm, step for step. Unlike
// path.Clean it keeps a trailing slash and repeated slashes, which name a
// different resource.
func removeDotSegments(in string) string {
	var out []string // output buffer, as segments each starting with "/"
	for in != "" {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[len("../"):]
		case strings.HasPrefix(in, "./"):
			in = in[len("./"):]
		case strings.HasPrefix(in, "/./"):
			in = in[len("/."):]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[len("/.."):]
			out = dropLast(out)
		case in == "/..":
			in = "/"
			out = dropLast(out)
		case in == "." || in == "..":
			in = ""
		default:
			// Move the first segment, with its leading "/" if any, to out.
			start := 0
			if in[0] == '/' {
				start = 1
			}
			end := strings.IndexByte(in[start:], '/')
			if end < 0 {
				end = len(in)
			} else {
				end += start
			}
			out = append(out, in[:end])
			in = in[end:]
		}
	}
	return strings.Join(out, "")
}

func dropLast(segments []string) []string {
	if len(segments) == 0 {
		return segments
	}
	return segments[:len(segments)-1]
}
