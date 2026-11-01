package memstore

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The steps of a FuzzRecords script. A step is two bytes: the first picks
// the step, modulo recordSteps; the second is its argument, whose bits each
// step reads as it says.
const (
	// recordStepInsert records a capability: under none, or under one
	// already recorded (the argument modulo one more than their count),
	// living recordFuzzUnit times the argument's top three bits and one,
	// no longer than its parent.
	recordStepInsert = iota
	// recordStepRevoke revokes a capability (the argument modulo their
	// count), and those below it when the argument's top bit is set.
	recordStepRevoke
	// recordStepAdvance moves the clock recordFuzzUnit times the
	// argument's low three bits and one.
	recordStepAdvance
	// recordStepPurge purges, at most the argument's low two bits and one
	// records.
	recordStepPurge
	// recordStepClaim claims a subject token (the argument's low two bits)
	// for a capability (its next three, modulo their count), the token
	// living recordFuzzUnit times its top three bits.
	recordStepClaim
	recordSteps
)

const (
	recordStepSize = 2
	// recordFuzzMaxSteps bounds one script, recordFuzzMaxNodes the
	// capabilities it records.
	recordFuzzMaxSteps = 256
	recordFuzzMaxNodes = 16
	// recordFuzzUnit is the grain of every time the script picks: a few
	// of them reach past limes.MinPurgeRetention.
	recordFuzzUnit = limes.MinPurgeRetention / 2
	// recordFuzzTokens is how many subject tokens the script can claim.
	recordFuzzTokens = 4
	// recordFuzzIssuer is the subject tokens' issuer.
	recordFuzzIssuer = "https://idp.example"

	recordArgLowBits  = 3
	recordArgLowMask  = 1<<recordArgLowBits - 1
	recordArgTopShift = 5
	recordArgTopMask  = 1<<3 - 1
	recordArgTopBit   = 1 << 7
	recordTokenMask   = recordFuzzTokens - 1
	recordTokenBits   = 2
	recordLimitMask   = 1<<2 - 1
)

// recordNode is a capability the script recorded, as the model holds it.
type recordNode struct {
	id       uuid.UUID
	parent   int
	expires  time.Time
	revoked  bool
	onRecord bool
}

// recordToken is a subject token claimed, as the model holds it: the
// capability it was exchanged for, when it expires, and whether a Purge
// may have dropped it — one whose retention ran past its expiry.
type recordToken struct {
	node      int
	expires   time.Time
	claimed   bool
	mayBeGone bool
}

// FuzzRecords runs a script of inserts, revocations, purges and exchange
// claims on a store's records under a moving clock, and holds the store,
// after every step, to limes.Store's contract as a model of it keeps it:
// IsRevoked answers for a capability and every ancestor still on record;
// Purge removes only capabilities expired past the retention, never one
// whose child stays on record, and leaves none due when it comes back
// short of its limit; an exchange's record outlives its capability until
// its subject token expires past the retention (ADR-0009).
func FuzzRecords(f *testing.F) {
	step := func(s, arg int) []byte { return []byte{byte(s), byte(arg)} }
	top := func(n int) int { return n << recordArgTopShift }
	// A root, a child, a grandchild; the root revoked; time passes and
	// everything is purged, a record at a time.
	f.Add(slices.Concat(step(recordStepInsert, top(4)), step(recordStepInsert, 1|top(3)), step(recordStepInsert, 2|top(2)),
		step(recordStepRevoke, 0), step(recordStepAdvance, recordArgLowMask), step(recordStepAdvance, recordArgLowMask),
		step(recordStepPurge, 0), step(recordStepPurge, 0), step(recordStepPurge, 0)))
	// A token exchanged for a capability that is purged while the token
	// lives, then purged once it expires.
	f.Add(slices.Concat(step(recordStepInsert, top(1)), step(recordStepClaim, top(6)), step(recordStepAdvance, 2),
		step(recordStepPurge, recordLimitMask), step(recordStepClaim, top(6)), step(recordStepAdvance, recordArgLowMask),
		step(recordStepPurge, recordLimitMask)))
	// Siblings expiring together with their parent, a cascade revocation.
	f.Add(slices.Concat(step(recordStepInsert, top(2)), step(recordStepInsert, 1|top(7)), step(recordStepInsert, 1|top(7)),
		step(recordStepRevoke, recordArgTopBit), step(recordStepAdvance, recordArgLowMask), step(recordStepPurge, 0), step(recordStepPurge, 1)))
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > recordFuzzMaxSteps*recordStepSize {
			script = script[:recordFuzzMaxSteps*recordStepSize]
		}
		ctx := context.Background()
		now := time.Unix(1_800_000_000, 0).UTC()
		s, err := NewStore(StoreConfig{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		tenant := uuid.New()
		var nodes []recordNode
		var tokens [recordFuzzTokens]recordToken
		key := func(i int) limes.ExchangeKey {
			return limes.ExchangeKey{TenantID: tenant, Issuer: recordFuzzIssuer, TokenID: string(rune('a' + i)), TokenExpiresAt: tokens[i].expires}
		}
		for i := 0; i+recordStepSize <= len(script); i += recordStepSize {
			at, arg := i/recordStepSize, int(script[i+1])
			switch int(script[i]) % recordSteps {
			case recordStepInsert:
				nodes = insertRecord(ctx, t, s, tenant, nodes, now, arg, at)
			case recordStepRevoke:
				if len(nodes) > 0 {
					revokeRecord(ctx, t, s, nodes, arg, at)
				}
			case recordStepAdvance:
				now = now.Add(time.Duration(arg&recordArgLowMask+1) * recordFuzzUnit)
			case recordStepPurge:
				purgeRecords(ctx, t, s, nodes, tokens[:], now, arg, at)
			case recordStepClaim:
				if len(nodes) == 0 {
					break
				}
				i := arg & recordTokenMask
				n := (arg >> recordTokenBits & recordArgLowMask) % len(nodes)
				tok := &tokens[i]
				// A claim names the token's expiry: the first claim's
				// stands while its record does.
				expires := now.Add(time.Duration(arg>>recordArgTopShift&recordArgTopMask) * recordFuzzUnit)
				k := key(i)
				if !tok.claimed || tok.mayBeGone {
					k.TokenExpiresAt = expires
				}
				got, claimed, err := s.ClaimExchange(ctx, k, nodes[n].id)
				switch {
				case !nodes[n].onRecord:
					if !errors.Is(err, limes.ErrNotFound) {
						t.Fatalf("step %d: a claim for a capability off record = %v, %v, %v; want ErrNotFound", at, got, claimed, err)
					}
				case tok.claimed && (!tok.mayBeGone || !claimed):
					if err != nil || claimed || got != nodes[tok.node].id {
						t.Fatalf("step %d: a claim of a token held = %v, %v, %v; want its capability %v", at, got, claimed, err, nodes[tok.node].id)
					}
				default:
					// Never claimed, or its record purged: claimed afresh.
					if err != nil || !claimed || got != nodes[n].id {
						t.Fatalf("step %d: a first claim = %v, %v, %v; want %v claimed", at, got, claimed, err, nodes[n].id)
					}
					*tok = recordToken{node: n, expires: expires, claimed: true}
				}
			}
			checkRecords(ctx, t, s, nodes, at)
			checkExchanges(ctx, t, s, nodes, tokens[:], key, at)
		}
	})
}

// insertRecord records a capability as arg asks, if the script has room
// for one, and returns the model's nodes with it.
func insertRecord(ctx context.Context, t *testing.T, s *Store, tenant uuid.UUID, nodes []recordNode, now time.Time, arg, at int) []recordNode {
	t.Helper()
	if len(nodes) == recordFuzzMaxNodes {
		return nodes
	}
	n := recordNode{id: uuid.New(), parent: arg%(len(nodes)+1) - 1, onRecord: true}
	n.expires = now.Add(time.Duration(arg>>recordArgTopShift&recordArgTopMask+1) * recordFuzzUnit)
	c := limes.Capability{
		ID: n.id, IssuedAt: now, ExpiresAt: n.expires,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}},
	}
	if n.parent >= 0 {
		p := nodes[n.parent]
		c.ParentID = p.id
		if p.expires.Before(n.expires) {
			n.expires, c.ExpiresAt = p.expires, p.expires
		}
	}
	err := s.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
	switch {
	case n.parent >= 0 && !nodes[n.parent].onRecord:
		if err == nil {
			t.Fatalf("step %d: recorded a child of %v, off record", at, nodes[n.parent].id)
		}
		return nodes
	case !n.expires.After(now):
		// Already expired: the store may refuse it.
		if err != nil {
			return nodes
		}
	case err != nil:
		t.Fatalf("step %d: Insert: %v", at, err)
	}
	return append(nodes, n)
}

// revokeRecord revokes the capability arg names, and those below it when
// it asks, in the store and the model.
func revokeRecord(ctx context.Context, t *testing.T, s *Store, nodes []recordNode, arg, at int) {
	t.Helper()
	n := arg % len(nodes)
	cascade := arg&recordArgTopBit != 0
	_, err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: nodes[n].id, CascadeChildren: cascade})
	if !nodes[n].onRecord {
		if !errors.Is(err, limes.ErrNotFound) {
			t.Fatalf("step %d: revoking a capability off record: %v, want ErrNotFound", at, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("step %d: Revoke: %v", at, err)
	}
	for i := range nodes {
		if i == n || cascade && nodes[i].onRecord && descends(nodes, i, n) {
			nodes[i].revoked = true
		}
	}
}

// descends reports whether node i is below node n.
func descends(nodes []recordNode, i, n int) bool {
	for p := nodes[i].parent; p >= 0; p = nodes[p].parent {
		if p == n {
			return true
		}
	}
	return false
}

// purgeRecords purges at most the limit arg asks and holds the result to
// Purge's contract, then brings the model up to what the store kept.
func purgeRecords(ctx context.Context, t *testing.T, s *Store, nodes []recordNode, tokens []recordToken, now time.Time, arg, at int) {
	t.Helper()
	req := limes.PurgeRequest{Limit: arg&recordLimitMask + 1}
	got, err := s.Purge(ctx, req)
	if err != nil {
		t.Fatalf("step %d: Purge: %v", at, err)
	}
	cutoff := now.Add(-req.Retention())
	var purged int64
	for i := range nodes {
		if !nodes[i].onRecord {
			continue
		}
		_, err := s.GetRecord(ctx, nodes[i].id)
		if errors.Is(err, limes.ErrNotFound) {
			if !nodes[i].expires.Before(cutoff) {
				t.Fatalf("step %d: purged %v, expiring %v, not past the cutoff %v", at, nodes[i].id, nodes[i].expires, cutoff)
			}
			nodes[i].onRecord = false
			purged++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if got.Purged != purged {
		t.Fatalf("step %d: Purge reported %d, removed %d", at, got.Purged, purged)
	}
	for i, n := range nodes {
		if n.onRecord && n.parent >= 0 && !nodes[n.parent].onRecord {
			t.Fatalf("step %d: purged %v while its child %v stays on record", at, nodes[n.parent].id, nodes[i].id)
		}
		if n.onRecord && n.expires.Before(cutoff) && purged < int64(req.Limit) {
			t.Fatalf("step %d: a batch of %d short of its limit %d left %v due", at, purged, req.Limit, n.id)
		}
	}
	for i := range tokens {
		if tokens[i].claimed && tokens[i].expires.Before(cutoff) {
			tokens[i].mayBeGone = true
		}
	}
}

// checkRecords holds IsRevoked to the model: a capability on record is
// revoked when it or an ancestor still on record is, and one off record is
// not revoked.
func checkRecords(ctx context.Context, t *testing.T, s *Store, nodes []recordNode, at int) {
	t.Helper()
	for i, n := range nodes {
		want := false
		if n.onRecord {
			for j := i; j >= 0 && nodes[j].onRecord; j = nodes[j].parent {
				want = want || nodes[j].revoked
			}
		}
		got, err := s.IsRevoked(ctx, n.id)
		if err != nil || got != want {
			t.Fatalf("step %d: IsRevoked(%v) = %v, %v; want %v (on record %v)", at, n.id, got, err, want, n.onRecord)
		}
	}
}

// checkExchanges holds ExchangedFor to the model: a token claimed answers
// with its capability, on record or not, until a Purge may have dropped it.
func checkExchanges(ctx context.Context, t *testing.T, s *Store, nodes []recordNode, tokens []recordToken, key func(int) limes.ExchangeKey, at int) {
	t.Helper()
	for i, tok := range tokens {
		got, err := s.ExchangedFor(ctx, key(i))
		switch {
		case !tok.claimed:
			if !errors.Is(err, limes.ErrNotFound) {
				t.Fatalf("step %d: ExchangedFor of a token never claimed = %v, %v; want ErrNotFound", at, got, err)
			}
		case tok.mayBeGone && errors.Is(err, limes.ErrNotFound):
		case err != nil || got != nodes[tok.node].id:
			t.Fatalf("step %d: ExchangedFor of a token claimed = %v, %v; want %v", at, got, err, nodes[tok.node].id)
		}
	}
}
