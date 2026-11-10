package httpsurl

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// testPassword is the password the cases below carry, which no refusal may
// repeat.
const testPassword = "s3cr%t!"

func TestParse(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want error
	}{
		"https with a host":            {raw: "https://idp.example/jwks"},
		"https with a port":            {raw: "https://idp.example:8443/jwks"},
		"http":                         {raw: "http://idp.example/jwks", want: ErrNotHTTPS},
		"no host":                      {raw: "https:///jwks", want: ErrNotHTTPS},
		"relative":                     {raw: "/jwks", want: ErrNotHTTPS},
		"a malformed escape":           {raw: "https://idp.example/%zz", want: ErrMalformedEscape},
		"a malformed escape, password": {raw: "https://user:" + testPassword + "@idp.example/jwks", want: ErrMalformedEscape},
		"http with a password":         {raw: "http://user:secret@idp.example/jwks", want: ErrNotHTTPS},
		"a mistyped scheme, password":  {raw: "htps//user:secret@idp.example/jwks", want: ErrNotHTTPS},
	} {
		t.Run(name, func(t *testing.T) {
			u, err := Parse(tc.raw)
			if tc.want == nil {
				if err != nil || u.String() != tc.raw {
					t.Fatalf("Parse(%q) = %v, %v", tc.raw, u, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse(%q) = %v, want %v", tc.raw, err, tc.want)
			}
			for _, secret := range []string{testPassword, "%t!", "secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Parse(%q) = %v, which repeats %q", tc.raw, err, secret)
				}
			}
		})
	}
}

// A control character is url.Parse's own refusal, kept, as it quotes no
// part of the URL.
func TestParseKeepsAReasonThatQuotesNothing(t *testing.T) {
	_, err := Parse("https://idp.example/\x7f")
	if err == nil || errors.Is(err, ErrNotHTTPS) || errors.Is(err, ErrMalformedEscape) || strings.Contains(err.Error(), "idp.example") {
		t.Fatalf("Parse of a control character = %v", err)
	}
}

// redirect is the CheckRedirect of SameOrigin(c) asked about a redirect to
// to after the requests via.
func redirect(t *testing.T, c *http.Client, to string, via ...string) error {
	t.Helper()
	req := &http.Request{URL: mustParse(t, to)}
	prior := make([]*http.Request, len(via))
	for i, v := range via {
		prior[i] = &http.Request{URL: mustParse(t, v)}
	}
	return SameOrigin(c).CheckRedirect(req, prior)
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

const (
	originURL  = "https://idp.example/jwks"
	movedURL   = "https://IDP.example/keys"
	otherHost  = "https://evil.example/jwks"
	plainHTTP  = "http://idp.example/jwks"
	otherPort  = "https://idp.example:8443/jwks"
	secretHost = "https://user:secret@evil.example/jwks"
)

func TestSameOrigin(t *testing.T) {
	c := &http.Client{}
	if err := redirect(t, c, movedURL, originURL); err != nil {
		t.Errorf("a redirect within the origin, its host in another case: %v", err)
	}
	for _, to := range []string{otherHost, plainHTTP, otherPort, secretHost} {
		err := redirect(t, c, to, originURL)
		if !errors.Is(err, ErrLeavesOrigin) || strings.Contains(err.Error(), "secret") {
			t.Errorf("redirect to %s: %v, want ErrLeavesOrigin without the password", to, err)
		}
	}
	if c.CheckRedirect != nil {
		t.Error("SameOrigin changed the caller's client")
	}
}

func TestSameOriginStopsAtMaxRedirects(t *testing.T) {
	via := make([]string, MaxRedirects)
	for i := range via {
		via[i] = originURL
	}
	if err := redirect(t, &http.Client{}, originURL, via[:MaxRedirects-1]...); err != nil {
		t.Errorf("redirect %d: %v", MaxRedirects, err)
	}
	if err := redirect(t, &http.Client{}, originURL, via...); !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("redirect %d: %v, want ErrTooManyRedirects", MaxRedirects+1, err)
	}
}

// A client's own policy decides within the origin, and never past it.
func TestSameOriginDefersToTheClientsPolicy(t *testing.T) {
	errOwn := errors.New("own policy")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errOwn }}
	if err := redirect(t, c, movedURL, originURL); !errors.Is(err, errOwn) {
		t.Errorf("within the origin: %v, want the client's own", err)
	}
	if err := redirect(t, c, otherHost, originURL); !errors.Is(err, ErrLeavesOrigin) {
		t.Errorf("past the origin: %v, want ErrLeavesOrigin", err)
	}
}
