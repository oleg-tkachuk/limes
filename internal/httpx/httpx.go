// Package httpx names the HTTP header fields and Cache-Control directives the
// module's packages write and read, so each is spelt in one place.
package httpx

// Header field names (RFC 9110, RFC 9111).
const (
	HeaderAccept        = "Accept"
	HeaderAge           = "Age"
	HeaderAllow         = "Allow"
	HeaderCacheControl  = "Cache-Control"
	HeaderContentLength = "Content-Length"
	HeaderContentType   = "Content-Type"
	HeaderETag          = "ETag"
	HeaderIfNoneMatch   = "If-None-Match"
)

// Cache-Control directives (RFC 9111 §5.2).
const (
	DirectiveMaxAge  = "max-age"
	DirectiveNoCache = "no-cache"
	DirectiveNoStore = "no-store"
	DirectivePublic  = "public"
)
