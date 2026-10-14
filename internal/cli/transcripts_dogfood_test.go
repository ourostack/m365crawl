package cli

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestFetchCommandDogfoodSharePointParts(t *testing.T) {
	e := textEnv(t)
	e.emptyArchive()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i, notice := range []struct{ call, chunk, types, stamp, item string }{
		{"dogfood-call-one", "0", "Recording+Transcript", "2026-11-03T10:00:00Z", "D1"},
		{"dogfood-call-two", "10001", "Transcript", "2026-11-04T10:00:00Z", "D2"},
		{"dogfood-call-two", "0", "Recording+Transcript", "2026-11-04T10:01:00Z", "D3"},
	} {
		html := trNotice(notice.call, notice.chunk, notice.types, notice.stamp, "drive:"+notice.item)
		html = strings.ReplaceAll(html, "tenant.sharepoint.example.invalid/teams/site-a", "contoso-my.sharepoint-df.com/personal/ada_contoso_example")
		if _, err := db.Exec(`INSERT INTO messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, tenantA, userA, trWeekly, strconv.Itoa(i+1), notice.stamp, transcripts.TypeRecording, html, notice.stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Begin(context.Background())
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if _, err := sess.DeriveTranscriptParts(context.Background(), []string{trAccount}); err != nil {
		sess.Rollback()
		_ = st.Close()
		t.Fatal(err)
	}
	if err := sess.Commit(); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	page := newTrPage()
	closer, opts := fakeBrowser(t, page)
	for _, tt := range []struct {
		call, states string
		count        float64
	}{
		{"dogfood-call-one", "ok", 1},
		{"dogfood-call-two", "ok,ok", 2},
	} {
		m := trJSON(t, e, "transcripts", "fetch", tt.call)
		if m["source"] != "network" || m["browser"] != "edge" || m["fetched"] != tt.count ||
			m["unfetchable"] != float64(0) || m["failed"] != float64(0) || partStates(fetchParts(t, m, 0)) != tt.states {
			t.Fatalf("every part must reach normal authenticated retrieval: %v", m)
		}
	}
	if page.fetched() != "D1,D2,D3" || closer.n.Load() != 2 || !opts.Headless {
		t.Fatalf("wrong retrieval or cleanup: fetched %q, closed %d, headless %v", page.fetched(), closer.n.Load(), opts.Headless)
	}
	if !signinHostOK("contoso-my.sharepoint-df.com") || signinHostOK("contoso-my.sharepoint-df.com.attacker.invalid") {
		t.Fatal("sign-in must use the same narrow host admission")
	}
}
