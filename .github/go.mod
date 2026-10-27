// Keeps .github out of the limes module. Go leaves a directory with a go.mod
// of its own out of a module's zip, so `go get` does not download the CI
// scripts and workflows. Nothing here is Go: the path is under the reserved
// .invalid domain (RFC 2606) so that nothing can fetch it, and there is no go
// directive for a tool to update. examples/internal/modzip checks the zip.
module limes.invalid/github
