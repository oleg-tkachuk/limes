// Package exchange exchanges an identity provider's access token for a
// limes capability, by OAuth 2.0 Token Exchange (RFC 8693), as
// docs/adr/0003-oauth-token-exchange.md decides.
//
// An MCP server, or a gateway in front of one, receives the access token
// its clients got from their authorization server, and exchanges it on its
// own side for a capability: for its tool calls, and for the agents and
// services it calls on. Clients keep the OAuth flow they know; the
// capability carries the operations, resources, budgets and revocation the
// access token does not.
//
// An Exchanger does it in process. A Handler serves it as an RFC 8693
// token endpoint, for a gateway or a token service within one trust domain.
// What each exchange grants is the Policy's: there is none by default. The
// Exchanger holds every answer to the request: the capability's audience is
// drawn from the resources and audiences asked for, it ends no later than
// the subject token, a subject token is accepted only from the client it
// was issued to, and a DPoP proof binds the capability to the client's key
// (RFC 9449 §5). Config.Exchanges issues one capability per subject token,
// for a service that exchanges where it receives the token
// (docs/adr/0009-one-capability-per-subject-token.md).
//
// Subject tokens are verified by a SubjectVerifier. exchange/jwtverify's
// verifies JWT access tokens (RFC 9068) through jwx; a consumer with a JOSE
// stack of its own implements SubjectVerifier on it instead, and this
// package links no JOSE library (docs/adr/0010-exchange-jose-subpackage.md).
//
// Every refusal is an RFC 6749 §5.2 error: [ErrorOf] gives the status and
// code the Handler answers with, for a consumer that calls an Exchanger in
// process, and [ErrorCodes] lists them.
package exchange
