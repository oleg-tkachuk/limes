package exchange

import (
	"context"
	"time"
)

// Subject is a subject token a SubjectVerifier accepted: who it names, for
// which client, and until when.
type Subject struct {
	// Issuer and Subject are the token's iss and sub.
	Issuer, Subject string
	// ClientID is the client the token was issued to.
	ClientID string
	// Audience is the token's aud: the resources it was issued for.
	Audience []string
	// Scopes are the token's scope, split at spaces.
	Scopes []string
	// ID is the token's jti.
	ID string
	// IssuedAt and ExpiresAt are its iat and exp.
	IssuedAt, ExpiresAt time.Time
	// JKT is the RFC 7638 thumbprint of the key the token is bound to
	// (RFC 9449 cnf.jkt); empty for a bearer token.
	JKT string
	// Claims are all of the token's claims, for a policy that reads one of
	// its identity provider's own, such as an organisation. It is also where
	// a multi-tenant SubjectVerifier puts the tenant it resolved, under a
	// key of its own, for the Policy to check and to set as the issued
	// capability's Subject.TenantID: Subject names no tenant itself.
	Claims map[string]any
}

// SubjectVerifier verifies a subject token: its signature, its issuer and
// its lifetime. Whether its audience is the client presenting it is the
// Exchanger's to check, with the Client.
type SubjectVerifier interface {
	VerifySubject(ctx context.Context, token string) (Subject, error)
}
