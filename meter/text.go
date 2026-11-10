package meter

import "github.com/oleg-tkachuk/limes/internal/storable"

// checkText refuses, as limes.ErrInvalidRequest, texts a store cannot keep
// as they are: invalid UTF-8, or a NUL (internal/storable), as limes's own
// requests refuse them. what names the request; the text is not repeated.
func checkText(what string, texts ...string) error {
	if !storable.Text(texts...) {
		return invalidRequest("%s: text that is not valid UTF-8 or holds a NUL", what)
	}
	return nil
}
