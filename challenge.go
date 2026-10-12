package limes

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// How a protected resource answers a request this module refused: a status,
// a WWW-Authenticate challenge as RFC 6750 §3 (Bearer) and RFC 9449 §7.1
// (DPoP) define it, and an RFC 9457 problem details body. Every service
// built on the module then refuses alike, and a client reads the refusal the
// standards' way rather than one service's.
//
// The descriptions are fixed per kind of refusal: an error's own text can
// name kids, issuers or stores, which is for the server's log, not the
// client.

// headerWWWAuthenticate is the response header a challenge travels in.
const headerWWWAuthenticate = "WWW-Authenticate"

// mediaTypeProblem is RFC 9457 §3's media type for problem details.
const mediaTypeProblem = "application/problem+json"

// problemTypeBase prefixes the problem type URIs this module defines.
const problemTypeBase = "https://github.com/oleg-tkachuk/limes/problems/"

// Challenge parameters of RFC 6750 §3 and RFC 9449 §7.1.
const (
	authParamRealm            = "realm"
	authParamError            = "error"
	authParamErrorDescription = "error_description"
	authParamAlgs             = "algs"
)

// Error codes of RFC 6750 §3.1 and RFC 9449 §7.1, §8.
const (
	codeInvalidRequest    = "invalid_request"
	codeInvalidToken      = "invalid_token"
	codeInsufficientScope = "insufficient_scope"
	codeInvalidDPoPProof  = "invalid_dpop_proof"
	codeUseDPoPNonce      = "use_dpop_nonce"
)

// Problem is an RFC 9457 problem details object.
type Problem struct {
	// Type is a URI naming the kind of problem.
	Type string `json:"type"`
	// Title is a short, fixed summary of the kind.
	Title string `json:"title"`
	// Status repeats the HTTP status.
	Status int `json:"status"`
	// Detail explains this occurrence, in terms safe to show the client.
	Detail string `json:"detail,omitempty"`
	_      noCompare
}

// ErrorResponse is how to answer a refused request.
type ErrorResponse struct {
	// Status is the HTTP status.
	Status int
	// Challenges are the WWW-Authenticate values, one per scheme; none for a
	// refusal that is not about authentication.
	Challenges []string
	// DPoPNonce is the nonce to send in the DPoP-Nonce header, when the
	// refusal asks for one.
	DPoPNonce string
	// Problem is the body.
	Problem Problem
}

// refusal is how a protected resource answers one kind of refusal.
type refusal struct {
	status int
	code   string // RFC 6750 / RFC 9449 error code; "" for no challenge
	dpop   bool   // the challenge is DPoP's
	bare   bool   // challenge with no error code: the client sent nothing to fault
	slug   string // the problem type, after problemTypeBase
	title  string
}

// The answers, one per kind of refusal; refusalRules says which sentinel
// gets which.
var (
	answerKeysUnavailable = refusal{http.StatusServiceUnavailable, "", false, false, "keys-unavailable", "The issuer's keys cannot be fetched"}
	answerMalformed       = refusal{http.StatusBadRequest, codeInvalidRequest, false, false, "malformed-authorization", "The Authorization header is malformed"}
	answerNoCredentials   = refusal{http.StatusUnauthorized, "", false, true, "no-credentials", "Credentials are required"}
	answerNonce           = refusal{http.StatusUnauthorized, codeUseDPoPNonce, true, false, "dpop-nonce-required", "A DPoP proof with the server's nonce is required"}
	answerProof           = refusal{http.StatusUnauthorized, codeInvalidDPoPProof, true, false, "invalid-dpop-proof", "The DPoP proof is missing or invalid"}
	answerToken           = refusal{http.StatusUnauthorized, codeInvalidToken, false, false, "invalid-token", "The access token is invalid"}
	answerScope           = refusal{http.StatusForbidden, codeInsufficientScope, false, false, "insufficient-scope", "The token does not allow this request"}
	answerExhausted       = refusal{http.StatusForbidden, "", false, false, "budget-exhausted", "The token's request or budget limit is exhausted"}
)

// internalError answers any other error: the server's fault, said without
// its text.
var internalError = refusal{status: http.StatusInternalServerError, slug: "internal", title: "The request could not be authorised"}

// answerFor is the answer to err: that of the first rule it matches.
func answerFor(err error) refusal {
	if rule, ok := ruleFor(err); ok {
		return rule.answer
	}
	return internalError
}

// ErrorResponse is how to answer a request refused with err, presented
// under scheme. A refusal about the token is challenged under the scheme the
// client used (Bearer when it named none); one about the proof, under DPoP.
// A verifier with DPoP configured also advertises its algorithms in a DPoP
// challenge, and hands out a fresh nonce with use_dpop_nonce.
func (v *Verifier) ErrorResponse(ctx context.Context, err error, scheme AuthScheme) ErrorResponse {
	r := answerFor(err)
	resp := ErrorResponse{
		Status:  r.status,
		Problem: Problem{Type: problemTypeBase + r.slug, Title: r.title, Status: r.status},
	}
	if r.code == "" && !r.bare {
		return resp
	}
	dpopConfigured := v.cfg.DPoP != nil
	challengeDPoP := r.dpop || scheme == AuthSchemeDPoP
	if challengeDPoP {
		resp.Challenges = append(resp.Challenges, challenge(AuthSchemeDPoP, v.cfg.Realm, r.code, r.title, true))
	} else {
		resp.Challenges = append(resp.Challenges, challenge(AuthSchemeBearer, v.cfg.Realm, r.code, r.title, false))
		if dpopConfigured {
			resp.Challenges = append(resp.Challenges, challenge(AuthSchemeDPoP, v.cfg.Realm, "", "", true))
		}
	}
	if r.code == codeUseDPoPNonce {
		resp.DPoPNonce, _ = v.DPoPNonce(ctx) // without one the client retries and is refused again
	}
	return resp
}

// challenge is one WWW-Authenticate challenge (RFC 9110 §11.6.1): the scheme,
// then error and error_description (RFC 6750 §3) when there is an error, for
// DPoP the accepted algorithms (RFC 9449 §7.1), and the realm. The realm is
// what keeps a challenge with no error code valid: RFC 6750 §3 requires at
// least one auth-param after the scheme.
func challenge(scheme AuthScheme, realm, code, description string, withAlgs bool) string {
	var params []string
	if code != "" {
		params = append(params, authParam(authParamError, code), authParam(authParamErrorDescription, description))
	}
	if withAlgs {
		params = append(params, authParam(authParamAlgs, strings.Join(dpop.SigningAlgorithms(), " ")))
	}
	params = append(params, authParam(authParamRealm, realm))
	return string(scheme) + " " + strings.Join(params, ", ")
}

// authParam is name="value" as a quoted-string (RFC 9110 §5.6.4), with only
// the characters RFC 6750 §3 allows in a value.
func authParam(name, value string) string {
	clean := strings.Map(func(r rune) rune {
		if authParamChar(r) {
			return r
		}
		return -1
	}, value)
	return name + `="` + clean + `"`
}

// authParamChar reports whether RFC 6750 §3 allows r in an auth-param value:
// %x20-21 / %x23-5B / %x5D-7E.
func authParamChar(r rune) bool {
	return r == 0x20 || r == 0x21 || r >= 0x23 && r <= 0x5B || r >= 0x5D && r <= 0x7E
}

// WriteErrorResponse writes resp: its status, challenges, nonce and problem.
func WriteErrorResponse(w http.ResponseWriter, resp ErrorResponse) {
	h := w.Header()
	for _, c := range resp.Challenges {
		h.Add(headerWWWAuthenticate, c)
	}
	if resp.DPoPNonce != "" {
		h.Set(dpop.NonceHeader, resp.DPoPNonce)
	}
	body, err := json.Marshal(resp.Problem)
	if err != nil {
		http.Error(w, http.StatusText(resp.Status), resp.Status)
		return
	}
	h.Set(httpx.HeaderContentType, mediaTypeProblem)
	h.Set(httpx.HeaderContentLength, strconv.Itoa(len(body)))
	// No store: a refusal is about this request's credentials.
	h.Set(httpx.HeaderCacheControl, httpx.DirectiveNoStore)
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}
