package limes

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"
)

// logCapture is a JSON slog handler at DEBUG writing to a buffer, and the
// lines it holds.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// logger is a logger writing to c, DEBUG and up.
func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// text is everything c holds.
func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// lines are the records c holds, each decoded.
func (c *logCapture) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(c.text()) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// only is the one record c holds, failing the test on any other count.
func (c *logCapture) only(t *testing.T) map[string]any {
	t.Helper()
	lines := c.lines(t)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want 1:\n%s", len(lines), c.text())
	}
	return lines[0]
}

// reset drops what c holds.
func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Reset()
}

// wantAttrs fails the test unless rec holds want's level, message and every
// attribute want names, each with its value; a value of nil wants the key
// absent.
func wantAttrs(t *testing.T, what string, rec map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, ok := rec[k]
		switch {
		case v == nil && ok:
			t.Errorf("%s: %s = %v, want it absent", what, k, got)
		case v != nil && got != v:
			t.Errorf("%s: %s = %v, want %v (line %v)", what, k, got, v, rec)
		}
	}
}

// shareCaveats allow delegating, so a capability of them has children.
var shareCaveats = Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}}

// observedIssue is a request Issue grants, for a fresh tenant.
func observedIssue() IssueRequest {
	return IssueRequest{
		ClientID: testClientID, IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData}, Caveats: shareCaveats, TTL: time.Hour,
	}
}

// Every token the issuer mints is logged at INFO, naming its capability.
func TestIssuerLogsEachToken(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	var logs logCapture
	issuer.logger = logs.logger()
	ctx := context.Background()
	level := slog.LevelInfo.String()

	root, err := issuer.Issue(ctx, observedIssue())
	if err != nil {
		t.Fatal(err)
	}
	c := root.Capability
	wantAttrs(t, "issue", logs.only(t), map[string]any{
		slog.LevelKey: level, slog.MessageKey: msgIssued, logKeyOperation: string(IssuerOperationIssue),
		logKeyCapability: c.ID.String(), logKeyIssuer: c.Issuer, logKeySubject: c.Subject.Subject,
		logKeyTenant: c.Subject.TenantID.String(), logKeyClient: c.ClientID,
		logKeyExpiresAt: c.ExpiresAt.UTC().Format(time.RFC3339), logKeyParent: nil,
	})

	logs.reset()
	child, err := issuer.Delegate(ctx, DelegateRequest{Parent: asVerified(c), Subject: Principal{Type: PrincipalTypeAgent, Subject: "worker"}, Caveats: shareCaveats})
	if err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "delegate", logs.only(t), map[string]any{
		slog.LevelKey: level, logKeyOperation: string(IssuerOperationDelegate),
		logKeyCapability: child.Capability.ID.String(), logKeyParent: c.ID.String(), logKeySubject: "worker",
	})

	logs.reset()
	if _, err := issuer.Reissue(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "reissue", logs.only(t), map[string]any{
		slog.LevelKey: level, logKeyOperation: string(IssuerOperationReissue), logKeyCapability: c.ID.String(),
	})

	logs.reset()
	if _, err := issuer.Biscuit(ctx, BiscuitRequest{Capability: asVerified(c)}); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "biscuit", logs.only(t), map[string]any{
		slog.LevelKey: level, logKeyOperation: string(IssuerOperationBiscuit), logKeyCapability: c.ID.String(),
	})
}

// A request the issuer refuses is logged at WARN with its code; a call that
// fails for the server's own reasons, at ERROR.
func TestIssuerLogsRefusalsAndFailures(t *testing.T) {
	ctx := context.Background()
	missing := uuid.New()
	noClient := observedIssue()
	noClient.ClientID = ""
	tests := []struct {
		name  string
		store func(*memStore) IssuanceStore
		call  func(*Issuer) error
		want  map[string]any
	}{
		{"a request without a client", nil, func(i *Issuer) error { _, err := i.Issue(ctx, noClient); return err },
			map[string]any{
				slog.LevelKey: slog.LevelWarn.String(), slog.MessageKey: msgIssuerRefused,
				logKeyOperation: string(IssuerOperationIssue), logKeyCode: string(CodeInvalidRequest),
				logKeySubject: noClient.Subject.Subject, logKeyTenant: noClient.Subject.TenantID.String(),
			}},
		{"a reissue of an id not on record", nil, func(i *Issuer) error { _, err := i.Reissue(ctx, missing); return err },
			map[string]any{
				slog.LevelKey: slog.LevelWarn.String(), logKeyOperation: string(IssuerOperationReissue),
				logKeyCapability: missing.String(), logKeyCode: string(CodeNotFound),
			}},
		{"a delegation from no parent", nil, func(i *Issuer) error {
			_, err := i.Delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "w"}})
			return err
		}, map[string]any{
			slog.LevelKey: slog.LevelWarn.String(), logKeyOperation: string(IssuerOperationDelegate),
			logKeyCapability: nil, logKeyCode: string(CodeInvalidRequest),
		}},
		{"a store that fails to record", func(m *memStore) IssuanceStore { return failingInsert{m} },
			func(i *Issuer) error { _, err := i.Issue(ctx, observedIssue()); return err },
			map[string]any{
				slog.LevelKey: slog.LevelError.String(), slog.MessageKey: msgIssuerFailed,
				logKeyOperation: string(IssuerOperationIssue), logKeyCode: nil,
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer, _, store, _ := buildIssuerVerifier(t)
			if tc.store != nil {
				issuer.store = tc.store(store)
			}
			var logs logCapture
			issuer.logger = logs.logger()
			if err := tc.call(issuer); err == nil {
				t.Fatal("the call succeeded")
			}
			rec := logs.only(t)
			wantAttrs(t, tc.name, rec, tc.want)
			if _, ok := rec[logKeyError]; !ok {
				t.Errorf("%s: no %s attribute in %v", tc.name, logKeyError, rec)
			}
		})
	}
}

// Each of the issuer's events is called with what the call did.
func TestIssuerEvents(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	var issued []IssuedEvent
	var refused, failed []IssuerErrorEvent
	issuer.events = IssuerEvents{
		OnIssued:  func(_ context.Context, e IssuedEvent) { issued = append(issued, e) },
		OnRefused: func(_ context.Context, e IssuerErrorEvent) { refused = append(refused, e) },
		OnFailed:  func(_ context.Context, e IssuerErrorEvent) { failed = append(failed, e) },
	}
	issuer.logger = slog.New(slog.DiscardHandler)
	ctx := context.Background()

	root, err := issuer.Issue(ctx, observedIssue())
	if err != nil {
		t.Fatal(err)
	}
	c := root.Capability
	child, err := issuer.Delegate(ctx, DelegateRequest{Parent: asVerified(c), Subject: Principal{Type: PrincipalTypeAgent, Subject: "w"}, Caveats: shareCaveats})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Biscuit(ctx, BiscuitRequest{Capability: asVerified(c)}); err != nil {
		t.Fatal(err)
	}
	if len(issued) != 3 ||
		issued[0].Operation != IssuerOperationIssue || issued[0].Capability.ID != c.ID ||
		issued[1].Operation != IssuerOperationDelegate || issued[1].Capability.ID != child.Capability.ID || issued[1].Capability.ParentID != c.ID ||
		issued[2].Operation != IssuerOperationBiscuit || issued[2].Capability.ID != c.ID {
		t.Errorf("OnIssued got %+v", issued)
	}

	store.revoked[c.ID] = true
	if _, err := issuer.Reissue(ctx, c.ID); err == nil {
		t.Fatal("reissued a revoked capability")
	}
	if len(refused) != 1 || refused[0].Operation != IssuerOperationReissue || refused[0].CapabilityID != c.ID ||
		refused[0].Code != CodeRevoked || refused[0].Err == nil {
		t.Errorf("OnRefused got %+v", refused)
	}

	issuer.store = failingInsert{store}
	if _, err := issuer.Issue(ctx, observedIssue()); err == nil {
		t.Fatal("issued through a store that cannot record")
	}
	if len(failed) != 1 || failed[0].Operation != IssuerOperationIssue || failed[0].Code != "" || failed[0].Err == nil {
		t.Errorf("OnFailed got %+v", failed)
	}
	if len(issued) != 3 || len(refused) != 1 {
		t.Errorf("a failure called another event: %d issued, %d refused", len(issued), len(refused))
	}
}

// NewIssuer and NewVerifier wire their Logger and Events; a nil Logger
// logs to slog.Default().
func TestConfigWiresLoggerAndEvents(t *testing.T) {
	issuer, verifier, _, _ := buildIssuerVerifier(t)
	var logs logCapture
	calls := 0
	built, err := NewIssuer(IssuerConfig{
		Signer: issuer.signer, Store: issuer.store, IssuerName: issuer.issuerName,
		Logger: logs.logger(), Events: IssuerEvents{OnIssued: func(context.Context, IssuedEvent) { calls++ }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Issue(context.Background(), observedIssue()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(logs.lines(t)) != 1 {
		t.Errorf("OnIssued called %d times, %d lines logged; want 1 and 1", calls, len(logs.lines(t)))
	}

	cfg := verifier.cfg
	cfg.Logger = logs.logger()
	cfg.Events = VerifierEvents{OnRefused: func(context.Context, VerifierErrorEvent) { calls++ }}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	logs.reset()
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: "garbage", Audience: audienceData}); err == nil {
		t.Fatal("verified garbage")
	}
	if calls != 2 || len(logs.lines(t)) != 1 {
		t.Errorf("OnRefused called %d times in all, %d lines logged; want 2 and 1", calls, len(logs.lines(t)))
	}

	own := slog.New(slog.DiscardHandler)
	if orDefault(nil) != slog.Default() || orDefault(own) != own {
		t.Error("orDefault does not fall back to slog.Default() alone")
	}
}

// The verifier logs a token accepted at DEBUG; one refused after its
// signature verified at WARN, naming the capability and its kid; one refused
// before at DEBUG, naming none; a lookup that failed at ERROR.
func TestVerifierLogLevels(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		op     VerifierOperation
		setup  func(*Verifier, *memStore, Capability)
		call   func(*Verifier, string) error
		level  slog.Level
		msg    string
		reason Reason
		named  bool
	}{
		{"accepted", VerifierOperationVerify, nil, verifyData, slog.LevelDebug, msgVerified, "", true},
		{"authorized", VerifierOperationAuthorize, nil, func(v *Verifier, token string) error {
			_, err := v.Authorize(ctx, AuthorizeRequest{Verify: VerifyRequest{Token: token, Audience: audienceData}, Check: CheckRequest{Op: OpGet}})
			return err
		}, slog.LevelDebug, msgVerified, "", true},
		{"introspected", VerifierOperationIntrospect, nil, func(v *Verifier, token string) error {
			_, err := v.Introspect(ctx, token)
			return err
		}, slog.LevelDebug, msgVerified, "", true},
		{"garbage", VerifierOperationVerify, nil, func(v *Verifier, _ string) error { return verifyData(v, "not-a-token") },
			slog.LevelDebug, msgVerifierRefused, ReasonInvalidToken, false},
		{"revoked", VerifierOperationVerify, func(_ *Verifier, s *memStore, c Capability) { s.revoked[c.ID] = true },
			verifyData, slog.LevelWarn, msgVerifierRefused, ReasonRevoked, true},
		{"expired", VerifierOperationVerify, func(v *Verifier, _ *memStore, _ Capability) {
			v.cfg.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		}, verifyData, slog.LevelWarn, msgVerifierRefused, ReasonExpired, true},
		{"another audience", VerifierOperationVerify, nil, func(v *Verifier, token string) error {
			_, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceMCP})
			return err
		}, slog.LevelWarn, msgVerifierRefused, ReasonAudienceMismatch, true},
		{"introspected and revoked", VerifierOperationIntrospect, func(_ *Verifier, s *memStore, c Capability) { s.revoked[c.ID] = true },
			func(v *Verifier, token string) error { _, err := v.Introspect(ctx, token); return err },
			slog.LevelWarn, msgVerifierRefused, ReasonRevoked, true},
		{"revocations down", VerifierOperationVerify, func(v *Verifier, _ *memStore, _ Capability) { v.cfg.Revocations = downRevocations{} },
			verifyData, slog.LevelError, msgVerifierFailed, ReasonRevocationUnavailable, true},
		{"key-bound, without its proof", VerifierOperationVerify, nil, verifyData,
			slog.LevelWarn, msgVerifierRefused, ReasonProofRequired, true},
		{"garbage as a Biscuit copy", VerifierOperationBiscuitCopy, nil, func(v *Verifier, _ string) error {
			_, err := v.BiscuitCopy(ctx, "not.a.token")
			return err
		}, slog.LevelDebug, msgVerifierRefused, ReasonInvalidToken, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer, v, store, _ := buildIssuerVerifier(t)
			req := observedIssue()
			if tc.reason == ReasonProofRequired {
				_, req.ConfirmationJKT = dpopKey(t)
			}
			res, err := issuer.Issue(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				tc.setup(v, store, res.Capability)
			}
			var logs logCapture
			v.cfg.Logger = logs.logger()
			if err := tc.call(v, res.Token); (err == nil) != (tc.reason == "") {
				t.Fatalf("call: %v", err)
			}
			want := map[string]any{slog.LevelKey: tc.level.String(), slog.MessageKey: tc.msg, logKeyOperation: string(tc.op)}
			if tc.reason != "" {
				want[logKeyReason] = string(tc.reason)
			}
			for _, k := range []string{logKeyCapability, logKeyIssuer, logKeyKeyID} {
				want[k] = nil
			}
			if tc.named {
				want[logKeyCapability], want[logKeyIssuer], want[logKeyKeyID] = res.Capability.ID.String(), res.Capability.Issuer, testKID
			}
			wantAttrs(t, tc.name, logs.only(t), want)
		})
	}
}

// verifyData verifies token for the data plane.
func verifyData(v *Verifier, token string) error {
	_, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
	return err
}

// A Biscuit names its capability and sealed kid in the log, through Verify
// and BiscuitCopy.
func TestVerifierLogsBiscuit(t *testing.T) {
	_, v, _, c, token := biscuitFixture(t)
	var logs logCapture
	v.cfg.Logger = logs.logger()
	if err := verifyData(v, token); err != nil {
		t.Fatal(err)
	}
	if _, err := v.BiscuitCopy(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	lines := logs.lines(t)
	if len(lines) != 2 {
		t.Fatalf("logged %d lines, want 2:\n%s", len(lines), logs.text())
	}
	for n, op := range []VerifierOperation{VerifierOperationVerify, VerifierOperationBiscuitCopy} {
		wantAttrs(t, string(op), lines[n], map[string]any{
			slog.MessageKey: msgVerified, logKeyOperation: string(op), logKeyCapability: c.ID.String(),
			logKeyKeyID: testKID, logKeyBiscuit: true,
		})
	}
}

// Each of the verifier's events is called with what the check found.
func TestVerifierEvents(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	var verified []VerifiedEvent
	var refused, failed []VerifierErrorEvent
	v.cfg.Events = VerifierEvents{
		OnVerified: func(_ context.Context, e VerifiedEvent) { verified = append(verified, e) },
		OnRefused:  func(_ context.Context, e VerifierErrorEvent) { refused = append(refused, e) },
		OnFailed:   func(_ context.Context, e VerifierErrorEvent) { failed = append(failed, e) },
	}
	v.cfg.Logger = slog.New(slog.DiscardHandler)
	res, err := issuer.Issue(context.Background(), observedIssue())
	if err != nil {
		t.Fatal(err)
	}
	c := res.Capability

	if err := verifyData(v, res.Token); err != nil {
		t.Fatal(err)
	}
	wantVerified := VerifiedEvent{Operation: VerifierOperationVerify, CapabilityID: c.ID, Issuer: c.Issuer, TenantID: c.Subject.TenantID, Subject: c.Subject.Subject}
	if len(verified) != 1 || verified[0] != wantVerified {
		t.Errorf("OnVerified got %+v, want %+v", verified, wantVerified)
	}

	if err := verifyData(v, "garbage"); err == nil {
		t.Fatal("verified garbage")
	}
	store.revoked[c.ID] = true
	if err := verifyData(v, res.Token); err == nil {
		t.Fatal("verified a revoked token")
	}
	if len(refused) != 2 {
		t.Fatalf("OnRefused got %+v, want 2 events", refused)
	}
	if e := refused[0]; e.Reason != ReasonInvalidToken || e.Code != CodeInvalidToken || e.Authenticated || e.CapabilityID != uuid.Nil() || e.Err == nil {
		t.Errorf("OnRefused for garbage got %+v", e)
	}
	if e := refused[1]; e.Reason != ReasonRevoked || !e.Authenticated || e.CapabilityID != c.ID || e.Issuer != c.Issuer || e.TenantID != c.Subject.TenantID {
		t.Errorf("OnRefused for a revoked token got %+v", e)
	}

	v.cfg.Revocations = downRevocations{}
	if err := verifyData(v, res.Token); err == nil {
		t.Fatal("verified with the revocations down")
	}
	if len(failed) != 1 || failed[0].Reason != ReasonRevocationUnavailable || failed[0].CapabilityID != c.ID {
		t.Errorf("OnFailed got %+v", failed)
	}
	if len(verified) != 1 || len(refused) != 2 {
		t.Errorf("a failure called another event: %d verified, %d refused", len(verified), len(refused))
	}
}

// No log line, at any level, holds a token, the token sealed in a Biscuit,
// or a signature of either: a capability is named by its id.
func TestLogsHoldNoToken(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	v.cfg.Biscuit = &BiscuitConfig{Revocations: store, Form: fakeForm{}}
	var logs logCapture
	issuer.logger, v.cfg.Logger = logs.logger(), logs.logger()
	ctx := context.Background()

	root, err := issuer.Issue(ctx, observedIssue())
	if err != nil {
		t.Fatal(err)
	}
	child, err := issuer.Delegate(ctx, DelegateRequest{Parent: asVerified(root.Capability), Subject: Principal{Type: PrincipalTypeAgent, Subject: "w"}, Caveats: shareCaveats})
	if err != nil {
		t.Fatal(err)
	}
	again, err := issuer.Reissue(ctx, root.Capability.ID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := issuer.Biscuit(ctx, BiscuitRequest{Capability: asVerified(root.Capability)})
	if err != nil {
		t.Fatal(err)
	}
	inner, _, err := fakeForm{}.Open(sealed.Token)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []string{root.Token, child.Token, again.Token, sealed.Token, inner}
	for _, token := range tokens[:4] {
		_ = verifyData(v, token)
		_, _ = v.Introspect(ctx, token)
		_, _ = v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceMCP})
	}
	copy, err := v.BiscuitCopy(ctx, sealed.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeBiscuit(ctx, RevokeBiscuitRequest{Copy: copy}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(ctx, RevokeRequest{CapabilityID: root.Capability.ID}); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens[:4] {
		_ = verifyData(v, token)
	}

	out := logs.text()
	if len(logs.lines(t)) == 0 {
		t.Fatal("nothing was logged")
	}
	for _, token := range tokens {
		secrets := []string{token}
		if parts := strings.Split(token, "."); len(parts) == 3 {
			secrets = append(secrets, parts[2])
		}
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("the log holds a token or its signature %q", s)
			}
		}
	}
}
