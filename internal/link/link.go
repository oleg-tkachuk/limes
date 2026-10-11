// Package link lets limes/inspect reach the root package's token reader
// without the root exporting it. The root sets DecodeToken as it
// initialises, which is before any code of inspect runs: inspect imports the
// root.
package link

// DecodeToken reads a compact capability token's header and claims without
// checking its signature, returning a limes.Capability.
var DecodeToken func(token string) (any, error)
