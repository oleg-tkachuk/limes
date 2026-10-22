package pgstore

import (
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// A row with no expiry — written before Insert refused one, or by hand —
// is expired to the census, the listing and the reader alike, not a record
// that never expires.
func TestARecordWithNoExpiryIsExpired(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := f.capability(f.mine)
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `UPDATE limes.capabilities SET expires_at = NULL WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	census, err := NewCensus(Config{DB: app, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if l, err := census.GetRecordListing(ctx, c.ID); err != nil || l.Disposition != limes.RecordDispositionExpired {
		t.Errorf("GetRecordListing = %s, %v; want expired", l.Disposition, err)
	}
	page, err := census.ListRecords(ctx, limes.ListRecordsRequest{TenantID: f.mine, Dispositions: []limes.RecordDisposition{limes.RecordDispositionExpired}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Record.Capability.ID != c.ID {
		t.Errorf("expired records = %+v, %v; want only %s", page.Items, err, c.ID)
	}
	counts, err := census.CountRecords(ctx, limes.CountRecordsRequest{TenantID: f.mine})
	if err != nil || len(counts.Items) != 1 || counts.Items[0].Expired != 1 {
		t.Errorf("CountRecords = %+v, %v; want one expired", counts.Items, err)
	}
}
