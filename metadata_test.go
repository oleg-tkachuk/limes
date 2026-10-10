package limes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RFC 8414 §3.1: the well-known suffix goes between the host and the path.
func TestMetadataURL(t *testing.T) {
	cases := map[string]string{
		"https://issuer.example.com":         "https://issuer.example.com/.well-known/oauth-authorization-server",
		"https://issuer.example.com/":        "https://issuer.example.com/.well-known/oauth-authorization-server",
		"https://example.com/issuer1":        "https://example.com/.well-known/oauth-authorization-server/issuer1",
		"https://example.com:8443/tenant/a/": "https://example.com:8443/.well-known/oauth-authorization-server/tenant/a",
	}
	for issuer, want := range cases {
		if got, err := MetadataURL(issuer); err != nil || got != want {
			t.Errorf("MetadataURL(%q) = %q, %v; want %q", issuer, got, err, want)
		}
	}
	for _, bad := range []string{"http://issuer.example.com", "https://issuer.example.com?x=1", "https://issuer.example.com#f", "issuer", ""} {
		if _, err := MetadataURL(bad); !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("MetadataURL(%q): err = %v, want ErrInvalidMetadata", bad, err)
		}
	}
}

func TestNewIssuerMetadata(t *testing.T) {
	m, err := NewIssuerMetadata("https://issuer.example.com", "https://issuer.example.com/jwks")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.DPoPSigningAlgValuesSupported) == 0 || m.DPoPSigningAlgValuesSupported[0] != algEd25519 ||
		m.AuthorizationDetailsTypesSupported[0] != AuthorizationDetailsType {
		t.Errorf("metadata = %+v", m)
	}
	for _, alg := range m.DPoPSigningAlgValuesSupported {
		if alg == algEdDSA {
			t.Error("the deprecated EdDSA is advertised")
		}
	}
	if _, err := NewIssuerMetadata("https://issuer.example.com", "http://issuer.example.com/jwks"); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("an http jwks_uri: err = %v", err)
	}
	m.RevocationEndpoint = "ftp://x"
	if err := m.Validate(); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("an ftp revocation endpoint: err = %v", err)
	}
}

// A verifier discovers an issuer from its identifier alone, and refuses a
// document that names another issuer (RFC 8414 §3.3).
func TestDiscoverIssuer(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	issuer := srv.URL + "/tenant-a"
	served, err := NewIssuerMetadata(issuer, srv.URL+"/jwks")
	if err != nil {
		t.Fatal(err)
	}
	h, err := MetadataHandler(served)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/.well-known/oauth-authorization-server/tenant-a", h)
	mux.HandleFunc("/.well-known/oauth-authorization-server/impostor", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","jwks_uri":"` + srv.URL + `/jwks"}`))
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/extra", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `/extra","jwks_uri":"` + srv.URL + `/jwks","grant_types_supported":["x"]}`))
	})
	ctx := context.Background()
	got, err := DiscoverIssuer(ctx, srv.Client(), issuer)
	if err != nil || got.JWKSURI != served.JWKSURI || got.Issuer != issuer {
		t.Fatalf("DiscoverIssuer = %+v, %v", got, err)
	}
	if _, err := DiscoverIssuer(ctx, srv.Client(), srv.URL+"/impostor"); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("a document naming another issuer: err = %v, want ErrInvalidMetadata", err)
	}
	if _, err := DiscoverIssuer(ctx, srv.Client(), srv.URL+"/extra"); err != nil {
		t.Errorf("a document with members this module does not know: %v", err)
	}
	if _, err := DiscoverIssuer(ctx, srv.Client(), srv.URL+"/missing"); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("no document: err = %v, want ErrInvalidMetadata", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if rec.Header().Get("Content-Type") != mediaTypeJSON || !strings.Contains(rec.Body.String(), `"dpop_signing_alg_values_supported":["Ed25519","ES256"]`) {
		t.Errorf("served %s %s", rec.Header().Get("Content-Type"), rec.Body)
	}
	if _, err := MetadataHandler(IssuerMetadata{Issuer: "http://x"}); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("a handler for invalid metadata: err = %v", err)
	}
}
