package limes

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sharedSDKTestdata holds the cases this module shares with SDKs in other
// languages. This module is their source; an SDK tests against a copy.
var sharedSDKTestdata = "testdata"

// biscuitVocabularyFile is the attenuation vocabulary as the SDKs know it.
const biscuitVocabularyFile = "biscuit_vocabulary.json"

// Term kinds the shared spec names.
const (
	termString  = "string"
	termDate    = "date"
	termInteger = "integer"
)

type biscuitVocabulary struct {
	Authority struct {
		Fact      string `json:"fact"`
		RootClaim string `json:"root_claim"`
	} `json:"authority"`
	Facts               map[string]string `json:"facts"`
	ReplaceTogether     [][]string        `json:"replace_together"`
	BindThumbprintBytes int               `json:"bind_thumbprint_bytes"`
}

func readBiscuitVocabulary(t *testing.T) biscuitVocabulary {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sharedSDKTestdata, biscuitVocabularyFile))
	if err != nil {
		t.Fatalf("read shared vocabulary: %v", err)
	}
	var v biscuitVocabulary
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse shared vocabulary: %v", err)
	}
	return v
}

// The names the SDKs write must be the names applyAttenuation reads: a fact
// renamed here and not there would make every SDK-made token refused.
func TestBiscuitVocabularyMatchesSharedSpec(t *testing.T) {
	v := readBiscuitVocabulary(t)

	want := map[string]string{
		biscuitFactOp:             termString,
		biscuitFactResourcePrefix: termString,
		biscuitFactResourceURI:    termString,
		biscuitFactPlane:          termString,
		biscuitFactExpires:        termDate,
		biscuitFactBind:           termString,
		biscuitFactMaxRequests:    termInteger,
		biscuitFactMaxBudget:      termInteger,
	}
	if !reflect.DeepEqual(v.Facts, want) {
		t.Errorf("facts = %v, want %v", v.Facts, want)
	}
	if v.Authority.Fact != biscuitFactCapability {
		t.Errorf("authority fact = %q, want %q", v.Authority.Fact, biscuitFactCapability)
	}
	field, ok := reflect.TypeFor[jwtClaims]().FieldByName("BiscuitRoot")
	if !ok {
		t.Fatal("jwtClaims has no BiscuitRoot")
	}
	if claim, _, _ := strings.Cut(field.Tag.Get("json"), ","); v.Authority.RootClaim != claim {
		t.Errorf("root claim = %q, want %q", v.Authority.RootClaim, claim)
	}
	wantTogether := [][]string{{biscuitFactResourcePrefix, biscuitFactResourceURI}}
	if !reflect.DeepEqual(v.ReplaceTogether, wantTogether) {
		t.Errorf("replace_together = %v, want %v", v.ReplaceTogether, wantTogether)
	}
	if v.BindThumbprintBytes != sha256.Size {
		t.Errorf("bind_thumbprint_bytes = %d, want %d", v.BindThumbprintBytes, sha256.Size)
	}
}
