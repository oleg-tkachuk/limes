// Command instrument is a binary that wraps a usage store in otelmetric.Instrument and calls Bump on it, for
// TestInstrumentRequestsLinksBumpAlone to read the symbols of. It reflects
// on a method by name, as text/template does, which makes the linker keep
// every exported method of each type the binary converts to an interface:
// the worst case a consumer's binary meets.
package main

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

func main() {
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		panic(err)
	}
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		panic(err)
	}
	var counter meter.RequestCounter
	counter, err = otelmetric.Instrument(inner)
	if err != nil {
		panic(err)
	}
	_, err = counter.Bump(context.Background(), meter.BumpRequest{}, nil)
	fmt.Println(err, reflect.ValueOf(counter).MethodByName(os.Args[0]).IsValid())
}
