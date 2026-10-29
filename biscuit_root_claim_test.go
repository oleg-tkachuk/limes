package limes

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// biscuitVocabulary is the shared Biscuit vocabulary the biscuit module and
// every SDK test their names against.
var biscuitVocabulary = filepath.Join("biscuit", "testdata", "biscuit_vocabulary.json")

// The sealed JWT names its Biscuit's root under the claim the shared
// vocabulary says: a claim renamed here and not there would make every
// Biscuit an SDK checks refused.
func TestBiscuitRootClaimMatchesSharedSpec(t *testing.T) {
	raw, err := os.ReadFile(biscuitVocabulary)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Authority struct {
			RootClaim string `json:"root_claim"`
		} `json:"authority"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	field, ok := reflect.TypeFor[jwtClaims]().FieldByName("BiscuitRoot")
	if !ok {
		t.Fatal("jwtClaims has no BiscuitRoot")
	}
	if claim, _, _ := strings.Cut(field.Tag.Get("json"), ","); v.Authority.RootClaim != claim || claim == "" {
		t.Errorf("root claim = %q, the sealed JWT's is %q", v.Authority.RootClaim, claim)
	}
}
