package outlookattendees

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestCollectExtendedEventGeometry(t *testing.T) {
	e := hxbuild.NewObject(0x6b, 0x459, 1113)
	e.PutU32(20, 6)
	e.PutU32(180, 8)
	got, err := Collect(context.Background(), source(t, e, detail(8), attendeeObject(3, 8, nil, nil)))
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].DetailKey != 8 || got.Evidence[0].EventLinks != 1 {
		t.Fatalf("extended layout lost unchanged detail link: %+v %v", got, err)
	}
}
