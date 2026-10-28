package pgstore

import (
	"encoding/base64"
	"encoding/json"
)

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
