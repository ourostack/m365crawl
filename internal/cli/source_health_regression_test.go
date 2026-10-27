package cli

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestReview248CalendarEventHealthCoversMailItActuallyReads(t *testing.T) {
	e := calEnv(t)
	root := t.TempDir()
	m := agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA)
	review := itemBySubject(t, m, "Fixture planning review")
	id := review["event_id"].(string)
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := st.CalendarEvent(ctx, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	msg := mailMsg(700, fInbox, "<review@example.test>", "", detail.Subject, 1, true)
	msg.ICalUID = detail.ICalUID
	if msg.ICalUID == "" {
		msg.Subject, msg.Received = detail.Subject, detail.Start
	}
	msg.Account = "outlook/Other"
	_, err = st.CommitMail(ctx, store.MailBatch{
		Account: msg.Account, ReadAt: mailT0, FreshAt: mailT0,
		Result: outlookmail.Result{Messages: []outlookmail.Message{msg}, Folders: mailFolderList()},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = st.RecordRun(ctx, store.Run{Source: "outlook|Other|mail", Status: "failed", ErrorCode: "outlook_mail_layout_unsupported", StartedAt: mailT0, FinishedAt: mailT0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	related, err := st.EventMail(ctx, detail.Event, store.EventMailLimit)
	if err != nil || len(related) != 1 || related[0].Account != "outlook/Other" {
		t.Fatalf("reproduction did not read Other mail: %+v %v", related, err)
	}
	_ = st.Close()
	code, out, stderr := e.run("--json", "--max-age", "0", "--outlook-root", root, "calendar", "event", id)
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	doc := decode(t, out)
	health, _ := doc["source_health"].([]any)
	if len(health) != 1 {
		t.Fatalf("calendar event reads failed Other mailbox but reports health=%v", doc["source_health"])
	}
}
