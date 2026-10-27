package jwks

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// algsOf is the alg each entry of a JWKS document names, kid → alg.
func algsOf(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	var set struct {
		Keys []entry `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range set.Keys {
		out[e.Kid] = e.Alg
	}
	return out
}

func newKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// Each entry names the alg its key's tokens name: EdDSA where asked, Ed25519
// for every other key; and the set reads back whole under either.
func TestMarshalAlgorithms(t *testing.T) {
	keys := map[string]ed25519.PublicKey{"old": newKey(t), "new": newKey(t)}
	doc, err := MarshalAlgorithms(keys, map[string]string{"new": AlgorithmEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	if got := algsOf(t, doc); got["old"] != AlgorithmEd25519 || got["new"] != AlgorithmEdDSA {
		t.Errorf("algs %v", got)
	}
	read, err := Parse(doc)
	if err != nil || len(read) != len(keys) || !read["new"].Equal(keys["new"]) || !read["old"].Equal(keys["old"]) {
		t.Errorf("Parse = %v, %v; want both keys", read, err)
	}
	plain, err := Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	none, err := MarshalAlgorithms(keys, nil)
	if err != nil || string(none) != string(plain) {
		t.Errorf("MarshalAlgorithms with none named differs from Marshal: %s, %v", none, err)
	}
	for name, algs := range map[string]map[string]string{
		"a kid not in the set": {"gone": AlgorithmEdDSA},
		"another algorithm":    {"new": "ES256"},
		"a name in lower case": {"new": "eddsa"},
	} {
		if _, err := MarshalAlgorithms(keys, algs); err == nil {
			t.Errorf("%s was written", name)
		}
	}
}

// The handler names each key's alg as its config says, and the document's
// ETag follows the names as it follows the keys.
func TestHandlerAlgorithms(t *testing.T) {
	keys := map[string]ed25519.PublicKey{"k1": newKey(t)}
	algs := map[string]string{}
	h, err := NewHandler(HandlerConfig{
		Keys:       func() map[string]ed25519.PublicKey { return keys },
		Algorithms: func() map[string]string { return algs },
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		return w
	}
	before := get()
	if got := algsOf(t, before.Body.Bytes())["k1"]; got != AlgorithmEd25519 {
		t.Errorf("alg %q with none named", got)
	}
	algs["k1"] = AlgorithmEdDSA
	after := get()
	if got := algsOf(t, after.Body.Bytes())["k1"]; got != AlgorithmEdDSA {
		t.Errorf("alg %q, want %q", got, AlgorithmEdDSA)
	}
	if before.Header().Get("ETag") == after.Header().Get("ETag") {
		t.Error("the ETag did not change with the alg")
	}
	algs["k2"] = AlgorithmEdDSA
	if w := get(); w.Code != http.StatusInternalServerError {
		t.Errorf("an alg for no key: status %d", w.Code)
	}
}
