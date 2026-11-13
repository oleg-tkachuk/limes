package jwtverify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/jwks"
)

// fuzzKeysURL is the JWKS URL FuzzRemoteTransform's documents answer.
const fuzzKeysURL = "https://idp.example/jwks"

// FuzzRemoteTransform hands the reader of a fetched key set the documents
// an identity provider, or whoever sits on its path, may serve, and holds
// it to two answers: one past jwks.DefaultMaxDocumentBytes, or no JWK Set,
// is refused and recorded as no fetch; one it reads is a set no larger
// than the cap, recorded as fetched — never a panic, whatever the bytes.
func FuzzRemoteTransform(f *testing.F) {
	doc, err := json.Marshal(newIDP(f).keys)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{doc, []byte(`{"keys":[]}`), []byte(`{"keys":[{"kty":"OKP"}]}`), []byte(notAJWKSet), nil} {
		f.Add(seed)
	}
	// The request a fetch answers, as httprc hands it on: jwx names its URL
	// in its errors.
	req, err := http.NewRequestWithContext(f.Context(), http.MethodGet, fuzzKeysURL, http.NoBody)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r := &remote{now: time.Now}
		res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {jsonContentType}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}
		set, err := r.transform(t.Context(), res)
		if err != nil {
			if !r.lastFetched().IsZero() {
				t.Fatalf("a refused document (%v) was recorded as fetched", err)
			}
			return
		}
		if len(body) > jwks.DefaultMaxDocumentBytes {
			t.Fatalf("read a document of %d bytes, past the cap of %d", len(body), jwks.DefaultMaxDocumentBytes)
		}
		if set == nil || r.lastFetched().IsZero() {
			t.Fatalf("a document read gave set %v, fetched at %v", set, r.lastFetched())
		}
	})
}
