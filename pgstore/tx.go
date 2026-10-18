package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// txKey keys the transaction a charge commits on in the context onCharged
// receives.
type txKey struct{}

// withTx is ctx carrying tx, for onCharged.
func withTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFrom returns the transaction a charge commits on, from the context
// UsageStore.Charge and UsageStore.Settle hand onCharged: a write through it
// commits with the charge, or with it not at all. Any other context holds
// none, and TxFrom reports false; no other method of this package reads a
// transaction from its context.
func TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}
