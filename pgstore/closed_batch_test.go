package pgstore

import (
	"errors"
	"testing"
)

// A batch sent on a transaction onCharged no longer holds refuses every
// read with the guard's error, and began no transaction.
func TestClosedBatchRefusesEveryRead(t *testing.T) {
	refused := errors.New("refused")
	b := closedBatch{refused}
	if _, err := b.Exec(); !errors.Is(err, refused) {
		t.Errorf("Exec: %v, want %v", err, refused)
	}
	if _, err := b.Query(); !errors.Is(err, refused) {
		t.Errorf("Query: %v, want %v", err, refused)
	}
	if err := b.QueryRow().Scan(); !errors.Is(err, refused) {
		t.Errorf("QueryRow: %v, want %v", err, refused)
	}
	if err := b.Close(); !errors.Is(err, refused) {
		t.Errorf("Close: %v, want %v", err, refused)
	}
	if tx := b.Tx(); tx != nil {
		t.Errorf("Tx = %v, want none", tx)
	}
}
