package redisreplay

import "testing"

// FuzzMember holds member to naming one pair: two pairs of a thumbprint and
// an id that spell the same member are the same pair, so no proof's id can
// be read as another key's, and none slips past the replay check as new.
func FuzzMember(f *testing.F) {
	f.Add("abc", "1", "ab", "c1")
	f.Add("1:a", "b", "1", ":ab")
	f.Add("", "x", "x", "")
	f.Fuzz(func(t *testing.T, jkt1, jti1, jkt2, jti2 string) {
		if member(jkt1, jti1) == member(jkt2, jti2) && (jkt1 != jkt2 || jti1 != jti2) {
			t.Fatalf("(%q, %q) and (%q, %q) are both member %q", jkt1, jti1, jkt2, jti2, member(jkt1, jti1))
		}
	})
}
