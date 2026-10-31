package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQOtherAccountServerRowsCannotBreakNativeMailImport(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			s, _ := sourceMailIntegrityArchive(t)
			ctx := context.Background()
			must0(s.SetWorkIQEnabled(ctx, enabled))
			batch := mailBatch(mailT0, mailMsg(101, fInbox, "separate@example.test", "Independent", 0))
			batch.Account = "outlook/Independent"
			for i := range batch.Result.Messages {
				batch.Result.Messages[i].Account = batch.Account
			}
			if _, err := s.CommitMail(ctx, batch); err != nil {
				t.Fatal("account A's server rows blocked account B's native import", err)
			}
			row := must(s.MailGet(ctx, batch.Account, 101))
			if row.Subject != "Independent" || row.Provenance != "desktop_cache" ||
				rowCount(t, s, `select count(*) from workiq_rows where account_id=?`, batch.Account) != 0 {
				t.Fatal("unrelated native mail acquired server identity or lost its native fields")
			}
		})
	}
}

func TestWorkIQFreshTerminalCannotRefreshOldNonterminalData(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	fresh := mailT0.Add(48 * time.Hour)
	stage := func(resource string, data any, at time.Time) int64 {
		t.Helper()
		raw := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{
			map[string]any{"statusCode": 200, "data": data},
		}}}))
		page := must(workiq.DecodePage(ctx, raw))
		progress := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
		must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: resource,
			ExpectedRevision: progress.Revision, Page: page, ObservedAt: at}))
		var id int64
		must0(s.db.QueryRowContext(ctx, `select max(id) from workiq_pages`).Scan(&id))
		if done := must(s.PrepareWorkIQMailPage(ctx, attempt, id, 128)); !done {
			t.Fatal("actual page preparation did not finish")
		}
		return id
	}
	root := "/me/mailFolders?includeHiddenFolders=true"
	stage(root, map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}}, fresh)
	resource := "/me/mailFolders/inbox-id/messages/delta"
	stage(resource, map[string]any{"value": []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")},
		"@odata.nextLink": resource + "?$skiptoken=second"}, mailT0)
	stage(resource, map[string]any{"value": []any{}, "@odata.deltaLink": resource + "?$deltatoken=end"}, fresh)
	must0(s.SealWorkIQMailInventory(ctx, attempt, fresh))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, resource, 2))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, root, 1))
	for done := false; !done; {
		done = must(s.ConfirmWorkIQMailCensusIfFull(ctx, attempt, 128))
	}
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: fresh, MailProof: &proof}))
	var original string
	must0(s.db.QueryRowContext(ctx, `select source_data_at from workiq_mail_receipts`).Scan(&original))
	observed := must(workIQStoredTime(sql.NullString{String: original, Valid: true}))
	if observed == nil || !observed.Equal(mailT0) {
		t.Fatal("a fresh terminal page replaced the old nonterminal original observation")
	}
	must(s.db.ExecContext(ctx, `insert into sync_runs(started_at,finished_at,source,status,error_code,source_accounts_json)
values(?,?,?,'failed','outlook_mail_layout_unsupported',?)`,
		fmtTime(mailT0.Add(-time.Hour)), fmtTime(mailT0.Add(-time.Hour)),
		"outlook|"+strings.TrimPrefix(mailAcct, "outlook/")+"|mail", `["`+mailAcct+`"]`))
	old := workIQHealthNow
	workIQHealthNow = func() time.Time { return fresh.Add(time.Hour) }
	t.Cleanup(func() { workIQHealthNow = old })
	found := false
	for _, health := range must(s.SourceHealth(ctx)) {
		if health.Source == "outlook_mail" {
			found = true
			if health.State == "covered_by_server" || health.LastErrorCode != "outlook_mail_layout_unsupported" {
				t.Fatal("stale nonterminal data acquired fresh covering health or erased the native failure")
			}
		}
	}
	if !found {
		t.Fatal("the native failed source was omitted from health")
	}
}
