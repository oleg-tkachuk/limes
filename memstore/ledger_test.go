package memstore

// committedLedger is a copy of the committed charge ledger, so a test can
// assert that a rolled-back charge left no trace.
func (s *UsageStore[TX]) committedLedger() []ledgerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ledgerEntry(nil), s.ledger...)
}
