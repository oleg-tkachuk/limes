package limes

import (
	"fmt"

	"github.com/go-json-experiment/json"
)

// Rich Authorization Requests, after RFC 9396. A capability's caveats travel
// in the token as one entry of the authorization_details claim (§9.1), of
// this module's type: the operations as the common "actions" field (§2.2),
// every other caveat as a field the type defines. An authorization server
// that speaks RAR can carry the same object in its requests and responses.

// AuthorizationDetailsType is the RFC 9396 type of a capability's
// authorization detail. A URI, as §2 recommends, so it collides with no
// other type.
const AuthorizationDetailsType = "https://github.com/oleg-tkachuk/limes/capability"

// AuthorizationDetail is one entry of an authorization_details array: a
// capability's caveats under AuthorizationDetailsType.
type AuthorizationDetail struct {
	Type string `json:"type"`
	Caveats
}

// NewAuthorizationDetail is c as an authorization detail.
func NewAuthorizationDetail(c Caveats) AuthorizationDetail {
	return AuthorizationDetail{Type: AuthorizationDetailsType, Caveats: c}
}

// MarshalAuthorizationDetails writes c as an authorization_details array of
// one entry, as RFC 9396 §2 shapes it.
func MarshalAuthorizationDetails(c Caveats) ([]byte, error) {
	return json.Marshal([]AuthorizationDetail{NewAuthorizationDetail(c)})
}

// ParseAuthorizationDetails reads an authorization_details array back into
// caveats. It must hold exactly one entry, of AuthorizationDetailsType, with
// no member the type does not define: a detail of another type grants
// something this module cannot enforce, and an unknown member restricts
// something it cannot check.
func ParseAuthorizationDetails(raw []byte) (Caveats, error) {
	var details []AuthorizationDetail
	if err := json.Unmarshal(raw, &details, strictClaims); err != nil {
		return Caveats{}, fmt.Errorf("%w: authorization_details: %w", ErrInvalidCaveats, err)
	}
	return capabilityDetail(details)
}

// capabilityDetail is the caveats of the one capability detail in details.
func capabilityDetail(details []AuthorizationDetail) (Caveats, error) {
	if len(details) != 1 {
		return Caveats{}, fmt.Errorf("%w: authorization_details holds %d entries, want 1", ErrInvalidCaveats, len(details))
	}
	if details[0].Type != AuthorizationDetailsType {
		return Caveats{}, fmt.Errorf("%w: authorization detail of type %q, want %q", ErrInvalidCaveats, details[0].Type, AuthorizationDetailsType)
	}
	return details[0].Caveats, nil
}
