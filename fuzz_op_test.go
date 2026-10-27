package limes_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// fuzzOpIssuer names the issuer FuzzOpValidate's tokens come from, and
// fuzzOpAudience the service they are for.
const (
	fuzzOpIssuer   = "fuzz-issuer"
	fuzzOpAudience = "fuzz-service"
	// fuzzMaxOpBytes bounds the op a run tries: a token carrying a longer
	// one would be refused for its size, which says nothing of the op.
	fuzzMaxOpBytes = 1 << 10
)

// builtinOps are the operations limes defines, in the order an unknown op's
// error names them.
var builtinOps = []limes.Op{limes.OpGet, limes.OpPut, limes.OpList, limes.OpDelete, limes.OpShare, limes.OpManage}

// discardStore records nothing and revokes nothing, so a long fuzz run
// keeps no capability it issued.
type discardStore struct{}

func (discardStore) Insert(context.Context, limes.InsertRequest) error  { return nil }
func (discardStore) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

// wellFormedOp is Op.Validate's rule, written out: a built-in op, or
// "<namespace>:<name>", both non-empty, of printable non-space ASCII.
func wellFormedOp(op limes.Op) bool {
	if slices.Contains(builtinOps, op) {
		return true
	}
	ns, name, ok := strings.Cut(string(op), ":")
	if !ok || ns == "" || name == "" {
		return false
	}
	for _, r := range string(op) {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// FuzzOpValidate holds Op.Validate to its rule, its refusal of an
// unnamespaced name to naming the built-in ops, and the issuer to it: an op
// it accepts is signed and verified back unchanged, one it refuses is never
// signed.
func FuzzOpValidate(f *testing.F) {
	for _, op := range []string{"get", "share", "tool:search", "mcp:github/create_issue", "search", ":x", "x:", "a b:c", "ns:naïve", "ns:tab\there", "GET"} {
		f.Add(op)
	}
	key, err := limes.GenerateKey()
	if err != nil {
		f.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		f.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: discardStore{}, IssuerName: fuzzOpIssuer})
	if err != nil {
		f.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		f.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: discardStore{}, TrustedIssuers: []string{fuzzOpIssuer}})
	if err != nil {
		f.Fatal(err)
	}
	names := make([]string, len(builtinOps))
	for i, op := range builtinOps {
		names[i] = string(op)
	}
	builtinList := "the built-in ops are " + strings.Join(names, ", ")

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > fuzzMaxOpBytes {
			return
		}
		op := limes.Op(raw)
		err := op.Validate()
		if want := wellFormedOp(op); (err == nil) != want {
			t.Fatalf("Validate(%q) = %v, want well formed %v", raw, err, want)
		}
		if err != nil {
			if !errors.Is(err, limes.ErrInvalidCaveats) {
				t.Fatalf("Validate(%q) = %v, not ErrInvalidCaveats", raw, err)
			}
			if !strings.Contains(raw, ":") && !strings.Contains(err.Error(), builtinList) {
				t.Fatalf("Validate(%q) = %v, which does not name the built-in ops", raw, err)
			}
		}
		if op.Builtin() != slices.Contains(builtinOps, op) {
			t.Fatalf("Builtin(%q) = %v", raw, op.Builtin())
		}

		ctx := context.Background()
		tenant := uuid.New()
		issued, issueErr := issuer.Issue(ctx, limes.IssueRequest{
			ClientID: exampleClientID,
			IssuedBy: limes.Principal{Subject: "operator"},
			Subject:  limes.Principal{Type: limes.PrincipalTypeService, TenantID: tenant, Subject: "svc"},
			Audience: []string{fuzzOpAudience},
			TTL:      time.Minute,
			Caveats:  limes.Caveats{Ops: []limes.Op{op}},
		})
		if err != nil {
			if !errors.Is(issueErr, limes.ErrInvalidCaveats) {
				t.Fatalf("Issue with op %q = %v, want ErrInvalidCaveats", raw, issueErr)
			}
			return
		}
		if issueErr != nil {
			t.Fatalf("Issue with op %q: %v", raw, issueErr)
		}
		got, err := verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: fuzzOpAudience})
		if err != nil {
			t.Fatalf("Verify of a token with op %q: %v", raw, err)
		}
		if ops := got.Capability().Caveats.Ops; len(ops) != 1 || ops[0] != op {
			t.Fatalf("op %q verified back as %q", raw, ops)
		}
	})
}
