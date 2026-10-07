package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

var (
	acctA = teamsdesktop.Account{TenantID: "tenant-1", UserID: "aaaaaaaa-0000-0000-0000-000000000001", Locale: "en-us"}
	acctB = teamsdesktop.Account{TenantID: "tenant-2", UserID: "bbbbbbbb-0000-0000-0000-000000000002", Locale: "en-us"}
	base  = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

func selfMRI(a teamsdesktop.Account) string { return "8:orgid:" + a.UserID }

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "data", "m365crawl.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func msg(a teamsdesktop.Account, conv, id, text string, at time.Time) teamsdesktop.Message {
	return teamsdesktop.Message{
		TenantID: a.TenantID, UserID: a.UserID, ConversationID: conv, ID: id,
		SenderID: "8:orgid:other-" + id, SenderName: "Sender " + id, SentAt: at,
		MessageType: "RichText/Html", ContentType: "Text", ContentText: text, ContentHTML: "<p>" + text + "</p>",
		Version: 1, Raw: []byte(`{"id":"` + id + `"}`),
	}
}

func conv(a teamsdesktop.Account, id, kind, name string) teamsdesktop.Conversation {
	return teamsdesktop.Conversation{
		TenantID: a.TenantID, UserID: a.UserID, ID: id, Kind: kind, Title: name, DisplayName: name,
		Raw: []byte(`{}`),
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func must0(err error) {
	if err != nil {
		panic(err)
	}
}

func must2[A any](a A, b bool, err error) (A, bool) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func ids(rows []MessageRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func eqStrings(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }
