package memstore

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		return storetest.Env{Ctx: context.Background(), Store: New[struct{}](), Tenant: uuid.New()}
	})
}
