package apidoc

// A Category is one section of the reference: a concern a reader comes
// looking for, holding the packages, or the parts of one, that serve it.
type Category struct {
	ID      string
	Title   string
	Summary string
}

// The categories, in the order the reference gives them: the order
// docs/guide.md takes the concerns in.
var categories = []Category{
	{catCapabilities, "Capabilities and caveats", "What a capability is and carries: its principal, its caveats, its amounts and ceilings, and the checks an operation is held to."},
	{catIssuing, "Issuing, delegating and signing", "Minting a capability, narrowing it for a delegate, and the keys and signers behind both."},
	{catVerifying, "Verifying and authorizing", "Checking a presented token and authorizing a call with it."},
	{catInspection, "Inspecting a token", "Reading a token without trusting it, for debugging."},
	{catDPoP, "Proof of possession (DPoP)", "Binding a capability to a key (RFC 9449): proofs, nonces and the replay caches."},
	{catKeys, "Keys and JWKS", "Publishing the signing keys and resolving another issuer's (RFC 7517)."},
	{catBiscuit, "Biscuit", "The Biscuit form of a capability, which its holder attenuates offline."},
	{catRevocation, "Revocation", "Revoking capabilities, one, by match or a Biscuit copy, and caching the answer."},
	{catMetering, "Metering", "Request limits, budgets, holds, leases, the ledger and tenant ceilings."},
	{catExchange, "Token exchange", "Exchanging an identity provider's token for a capability (RFC 8693)."},
	{catStorage, "Storage", "The store contracts and the stores limes ships: in memory and PostgreSQL."},
	{catErrors, "Errors and refusals", "Error codes, categories and reasons, and answering a refusal over HTTP. docs/errors.md holds the tables."},
	{catObserving, "Observing", "Events the issuer, verifier, revoker and stores report, and their OpenTelemetry instruments."},
	{catConformance, "Conformance suites", "Test suites a store, cache or consumer of yours runs to prove it keeps the contract."},
}

// Category IDs: each package and each root file names one.
const (
	catCapabilities = "capabilities"
	catIssuing      = "issuing"
	catVerifying    = "verifying"
	catInspection   = "inspection"
	catDPoP         = "dpop"
	catKeys         = "keys"
	catBiscuit      = "biscuit"
	catRevocation   = "revocation"
	catMetering     = "metering"
	catExchange     = "exchange"
	catStorage      = "storage"
	catErrors       = "errors"
	catObserving    = "observing"
	catConformance  = "conformance"
)

// rootDir is the root package's directory, whose declarations fall in
// categories by their file (rootFiles); every other package falls in one.
const rootDir = "."

// A Package is one public package of the repository's modules.
type Package struct {
	// Dir is the package's directory from the repository's root.
	Dir string
	// Category is the package's category; empty for the root package.
	Category string
}

// packages are the public packages the reference documents, in the
// order a category lists them.
var packages = []Package{
	{rootDir, ""},
	{"inspect", catInspection},
	{"dpop", catDPoP},
	{"redisreplay", catDPoP},
	{"jwks", catKeys},
	{"biscuit", catBiscuit},
	{"meter", catMetering},
	{"exchange", catExchange},
	{"exchange/jwtverify", catExchange},
	{"memstore", catStorage},
	{"pgstore", catStorage},
	{"pgstore/migrate", catStorage},
	{"pgstore/migrate/ddl", catStorage},
	{"otelmetric", catObserving},
	{"limestest", catConformance},
	{"storetest", catConformance},
	{"metertest", catConformance},
	{"dpop/dpoptest", catConformance},
	{"jwks/jwkstest", catConformance},
	{"exchange/exchangetest", catConformance},
}

// rootFiles places each file of the root package that declares an
// exported name: a declaration falls in its file's category. A new file
// must be placed here, or the reference is not generated.
var rootFiles = map[string]string{
	"types.go":         catCapabilities,
	"principal.go":     catCapabilities,
	"ceiling.go":       catCapabilities,
	"nanos.go":         catCapabilities,
	"unit.go":          catCapabilities,
	"effect.go":        catCapabilities,
	"resource.go":      catCapabilities,
	"check.go":         catCapabilities,
	"validate.go":      catCapabilities,
	"meter_subject.go": catCapabilities,
	"delegate.go":      catCapabilities,
	"wire.go":          catCapabilities,

	"issuer.go":        catIssuing,
	"signer.go":        catIssuing,
	"keyloader.go":     catIssuing,
	"key_namespace.go": catIssuing,
	"redact.go":        catIssuing,

	"verifier.go":      catVerifying,
	"authorization.go": catVerifying,

	"sealed_form.go": catBiscuit,

	"revoker.go":                  catRevocation,
	"bulk_revoke.go":              catRevocation,
	"biscuit_revocation.go":       catRevocation,
	"biscuit_revocation_cache.go": catRevocation,
	"cache.go":                    catRevocation,

	"store.go":          catStorage,
	"store_as.go":       catStorage,
	"listing.go":        catStorage,
	"pages.go":          catStorage,
	"census.go":         catStorage,
	"erase_tenant.go":   catStorage,
	"exchange_store.go": catStorage,

	"errors.go":        catErrors,
	"store_failure.go": catErrors,
	"code.go":          catErrors,
	"category.go":      catErrors,
	"reason.go":        catErrors,
	"challenge.go":     catErrors,

	"events.go":           catObserving,
	"issuer_observe.go":   catObserving,
	"verifier_observe.go": catObserving,
	"revoker_observe.go":  catObserving,
}
