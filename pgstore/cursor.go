package pgstore

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// textNUL is the one character a JSON string can carry and a PostgreSQL
// text value cannot: a parameter holding it fails the whole statement.
const textNUL = "\x00"

// storableText reports whether every field can reach SQL as text, so a
// cursor carrying one that cannot is refused as the client's, rather than
// failing in PostgreSQL as the server's.
func storableText(fields ...string) bool {
	for _, f := range fields {
		if strings.Contains(f, textNUL) {
			return false
		}
	}
	return true
}

// encodeCursor is a cursor as the store hands it out: opaque, URL-safe.
func encodeCursor(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // the cursors are structs of uuids, strings, times and integers
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor reads a cursor encodeCursor wrote into v.
func decodeCursor(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
