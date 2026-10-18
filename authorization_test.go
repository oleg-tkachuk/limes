package limes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseAuthorization(t *testing.T) {
	cases := []struct {
		header string
		scheme AuthScheme
		token  string
	}{
		{"Bearer abc.def.ghi", AuthSchemeBearer, "abc.def.ghi"},
		{"bearer abc", AuthSchemeBearer, "abc"}, // RFC 9110 §11.1: case-insensitive
		{"DPoP abc-_~+/=", AuthSchemeDPoP, "abc-_~+/="},
		{"dpop  abc==", AuthSchemeDPoP, "abc=="},
	}
	for _, tc := range cases {
		scheme, token, err := ParseAuthorization(tc.header)
		if err != nil || scheme != tc.scheme || token != tc.token {
			t.Errorf("ParseAuthorization(%q) = %q, %q, %v; want %q, %q", tc.header, scheme, token, err, tc.scheme, tc.token)
		}
	}
	for bad, scheme := range map[string]AuthScheme{
		"Bearer": AuthSchemeBearer, "Bearer ": AuthSchemeBearer, "Bearer a b": AuthSchemeBearer, "Bearer =abc": AuthSchemeBearer,
		"Bearer a=b": AuthSchemeBearer, "Bearer a\tb": AuthSchemeBearer, "DPoP é": AuthSchemeDPoP, "dpop a b": AuthSchemeDPoP,
	} {
		// The scheme it recognised comes back, for the challenge to use.
		if got, _, err := ParseAuthorization(bad); !errors.Is(err, ErrMalformedAuthorization) || got != scheme {
			t.Errorf("ParseAuthorization(%q) = %q, %v; want %q and ErrMalformedAuthorization", bad, got, err, scheme)
		}
		// A malformed request, as ErrInvalidRequest's family is, and still
		// the client's to fix: 400, not the 500 of the server's own call.
		_, _, err := ParseAuthorization(bad)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("ParseAuthorization(%q): err = %v, want it to match ErrInvalidRequest", bad, err)
		}
		if got := (&Verifier{}).ErrorResponse(context.Background(), err, scheme).Status; got != http.StatusBadRequest {
			t.Errorf("ParseAuthorization(%q): ErrorResponse status = %d, want %d", bad, got, http.StatusBadRequest)
		}
	}
	// No header, or another scheme's: no credential of ours at all.
	for _, none := range []string{"", "Basic dXNlcjpwdw==", "Negotiate abc"} {
		if _, _, err := ParseAuthorization(none); !errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrMalformedAuthorization) {
			t.Errorf("ParseAuthorization(%q): err = %v, want ErrNoCredentials", none, err)
		}
	}
}

// An error is for the server's log, so it never repeats a credential: a
// header with no single space after the scheme is all credential, and the
// scheme name it would report is the token itself.
func TestParseAuthorizationKeepsTheCredentialOutOfItsError(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	for _, header := range []string{token, "Bearer\t" + token, "Bearer:" + token, "Bearer," + token, "DPoP;" + token, "sk_live_abcdef"} {
		_, _, err := ParseAuthorization(header)
		if !errors.Is(err, ErrNoCredentials) {
			t.Errorf("ParseAuthorization(%.20q…): err = %v, want ErrNoCredentials", header, err)
			continue
		}
		credential := header[strings.IndexAny(header, "\t:,;")+1:]
		if strings.Contains(err.Error(), credential) {
			t.Errorf("ParseAuthorization(%.20q…): the error repeats the credential: %v", header, err)
		}
	}
	// A scheme that is only a scheme is still named, for the log to say what came.
	for _, scheme := range []string{"Basic", "Negotiate", "AWS4-HMAC-SHA256"} {
		if _, _, err := ParseAuthorization(scheme + " abc"); err == nil || !strings.Contains(err.Error(), scheme) {
			t.Errorf("ParseAuthorization(%q): err = %v, want it to name the scheme", scheme+" abc", err)
		}
	}
}

// RFC 9449 §7.2: a key-bound token presented as Bearer is a downgrade and is
// refused even with a valid proof; an unbound one presented as DPoP claims a
// binding it does not have.
func TestVerifyHoldsTheSchemeToTheBinding(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	bound := issueBound(t, issuer, key)
	_, unbound := issueFor(t, issuer)
	cases := []struct {
		name   string
		token  string
		proof  bool
		scheme AuthScheme
		want   error
	}{
		{"bound as DPoP", bound, true, AuthSchemeDPoP, nil},
		{"bound as Bearer", bound, true, AuthSchemeBearer, ErrAuthSchemeMismatch},
		{"bound, no scheme", bound, true, AuthSchemeUnspecified, nil},
		{"unbound as Bearer", unbound, false, AuthSchemeBearer, nil},
		{"unbound as DPoP", unbound, false, AuthSchemeDPoP, ErrAuthSchemeMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := VerifyRequest{Token: tc.token, Audience: audienceData}
			if tc.proof {
				req = withProof(ctx, t, key, tc.token, audienceData, time.Now())
			}
			req.Scheme = tc.scheme
			_, err := v.Verify(ctx, req)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil && (!errors.Is(err, ErrProofOfPossession) || errors.Is(err, ErrInvalidToken)) {
				t.Fatalf("%v: want ErrProofOfPossession and not ErrInvalidToken", err)
			}
		})
	}
}

// A scheme ParseAuthorization never returns is the caller's mistake, refused
// before it is read as either binding.
func TestVerifyRefusesAnUnknownScheme(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	const basic AuthScheme = "Basic"
	_, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData, Scheme: basic})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

// Validate admits the schemes ParseAuthorization returns, and none, and
// refuses any other.
func TestAuthSchemeValidate(t *testing.T) {
	for _, s := range []AuthScheme{AuthSchemeUnspecified, AuthSchemeBearer, AuthSchemeDPoP} {
		if err := s.Validate(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []AuthScheme{"Basic", "bearer", "dpop"} {
		if err := s.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%q: err = %v, want ErrInvalidRequest", s, err)
		}
	}
}
