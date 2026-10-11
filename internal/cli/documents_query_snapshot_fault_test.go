package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsSharedQueryRefusesBrokenCurrentSourceSnapshot(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	e.exec(`alter table knowledge_locators rename column kind to broken`)
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	c := &searchCmd{Query: "Quarterly", Source: searchDocuments, msgFlags: msgFlags{Limit: 10}}
	filter, err := rt.filter(c.msgFlags)
	if err != nil {
		t.Fatal(err)
	}
	filter.Total = new(int)
	if _, err := c.search(rt, searchPlan{indexedDocuments: true}, filter, st); err == nil {
		t.Fatal("metadata hits became successful while their current source snapshot was unreadable")
	}
}
