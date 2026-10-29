package limes_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// Reissue signs the capability on record again: under the same key it is
// the token Issue returned, byte for byte, and it records nothing.
func TestReissueIsTheIssuedToken(t *testing.T) {
	s := newExampleStack()
	issued, token := s.issue()
	got, err := s.issuer.Reissue(context.Background(), issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != token {
		t.Errorf("Reissue token differs from Issue's:\n got %s\nwant %s", got.Token, token)
	}
	if !reflect.DeepEqual(got.Capability, issued) {
		t.Errorf("Reissue capability = %+v, want %+v", got.Capability, issued)
	}
	if vc := s.verified(got.Token).Capability(); vc.ID != issued.ID {
		t.Errorf("the reissued token verifies as %s, want %s", vc.ID, issued.ID)
	}
}

// Reissue refuses a capability not on record, revoked, or expired: a
// token is signed again only for one that is live.
func TestReissueRefusesWhatIsNotLive(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	if _, err := s.issuer.Reissue(ctx, uuid.New()); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("Reissue of an id not on record: err = %v, want ErrNotFound", err)
	}
	revoked, _ := s.issue()
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: revoked.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.issuer.Reissue(ctx, revoked.ID); !errors.Is(err, limes.ErrRevoked) {
		t.Errorf("Reissue of a revoked capability: err = %v, want ErrRevoked", err)
	}
}

// reissueClock is a clock a test moves.
type reissueClock struct{ now time.Time }

func (c *reissueClock) Now() time.Time { return c.now }

func TestReissueRefusesAnExpiredCapability(t *testing.T) {
	ctx := context.Background()
	clock := &reissueClock{now: time.Now()}
	records, err := memstore.NewStore(memstore.StoreConfig{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: exampleIssuerName, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	const ttl = time.Minute
	res, err := issuer.Issue(ctx, limes.IssueRequest{
		ClientID: exampleClientID, IssuedBy: limes.Principal{Subject: "operator"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Audience: []string{exampleAudience}, TTL: ttl,
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(ttl)
	if _, err := issuer.Reissue(ctx, res.Capability.ID); !errors.Is(err, limes.ErrExpired) {
		t.Errorf("Reissue of an expired capability: err = %v, want ErrExpired", err)
	}
}

// insertOnly is an IssuanceStore that reads no record back.
type insertOnly struct{ limes.IssuanceStore }

// An issuer whose store reads no record cannot reissue: it says so rather
// than sign what it cannot read.
func TestReissueNeedsAStoreThatReadsRecords(t *testing.T) {
	s := newExampleStack()
	issued, _ := s.issue()
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: insertOnly{s.records}, IssuerName: exampleIssuerName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Reissue(context.Background(), issued.ID); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Reissue through a store that reads no record: err = %v, want ErrInvalidRequest", err)
	}
}
