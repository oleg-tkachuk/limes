package limes

import (
	"context"
	"errors"
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
	for _, bad := range []string{"Bearer", "Bearer ", "Bearer a b", "Bearer =abc", "Bearer a=b", "Bearer a\tb", "DPoP é"} {
		if _, _, err := ParseAuthorization(bad); !errors.Is(err, ErrMalformedAuthorization) {
			t.Errorf("ParseAuthorization(%q): err = %v, want ErrMalformedAuthorization", bad, err)
		}
	}
	// No header, or another scheme's: no credential of ours at all.
	for _, none := range []string{"", "Basic dXNlcjpwdw==", "Negotiate abc"} {
		if _, _, err := ParseAuthorization(none); !errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrMalformedAuthorization) {
			t.Errorf("ParseAuthorization(%q): err = %v, want ErrNoCredentials", none, err)
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
				req = withProof(t, key, tc.token, audienceData, time.Now())
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
