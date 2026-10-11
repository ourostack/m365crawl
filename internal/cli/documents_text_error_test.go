package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsTextDoesNotHideNativeEncodingFailure(t *testing.T) {
	model := officedocuments.Document{Modified: officedocuments.Timestamp{
		Value: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	detail := documentDetailItem{documentItem: documentItemOf(store.DocumentObservationRow{ID: 1, Document: model}, 0)}
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := rt.renderText("docs show", newList([]any{detail}, false)); err == nil {
		t.Fatal("native JSON encoding failure produced success-shaped text detail")
	}
}
