package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

var (
	windowSince = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	windowUntil = time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
)

type mbMessage struct {
	ID       string `json:"id"`
	Folder   string `json:"folder"`
	Header   string `json:"header"`
	Received string `json:"received,omitempty"`
	Modified string `json:"modified"`
	Pad      int    `json:"pad,omitempty"`
}

type mbEvent struct {
	At int    `json:"at"`
	Op string `json:"op"`
	mbMessage
	To    string `json:"to,omitempty"`
	NewID string `json:"new_id,omitempty"`
}

type mbScenario struct {
	Folders  []map[string]any `json:"folders"`
	Messages []mbMessage      `json:"messages"`
	Events   []mbEvent        `json:"events,omitempty"`
	State    string           `json:"state"`
	Expire   []map[string]any `json:"expire,omitempty"`
	PauseAt  int              `json:"pause_at,omitempty"`
	Pause    string           `json:"pause_file,omitempty"`
}

func windowFolders() []map[string]any {
	return []map[string]any{
		{"id": "inbox-id", "name": "Inbox", "well_known": "inbox"},
		{"id": "sent-id", "name": "Sent Items", "well_known": "sentitems"},
		{"id": "drafts-id", "name": "Drafts", "well_known": "drafts"},
		{"id": "deleted-id", "name": "Deleted Items", "well_known": "deleteditems"},
		{"id": "junk-id", "name": "Junk Email", "well_known": "junkemail"},
		{"id": "archive-id", "name": "Archive", "well_known": "archive", "hidden": true},
		{"id": "child-id", "name": "Projects", "parent": "inbox-id"},
	}
}

func mbMail(id, folder string, received time.Time) mbMessage {
	return mbMessage{ID: id, Folder: folder, Header: "<" + id + "@example.test>", Received: received.UTC().Format(time.RFC3339),
		Modified: received.UTC().Format(time.RFC3339)}
}

// writeMailbox points the synthetic WorkIQ at a scenario mailbox.
func writeMailbox(t *testing.T, sc mbScenario) mbScenario {
	t.Helper()
	dir := t.TempDir()
	if sc.Folders == nil {
		sc.Folders = windowFolders()
	}
	sc.State = filepath.Join(dir, "state.json")
	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "mailbox.json")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_MAILBOX", name)
	return sc
}

// windowMessages is a week of mail in two folders with later and earlier mail around it: 3 in the
// inbox and 2 in a child folder inside the window, 12 later inbox items (two delta pages of their
// own) and 2 from before it.
func windowMessages() []mbMessage {
	var out []mbMessage
	for i := 0; i < 3; i++ {
		out = append(out, mbMail(fmt.Sprintf("in-%d", i), "inbox-id", windowSince.Add(time.Duration(i+1)*24*time.Hour)))
	}
	for i := 0; i < 2; i++ {
		out = append(out, mbMail(fmt.Sprintf("child-%d", i), "child-id", windowSince.Add(time.Duration(i+1)*36*time.Hour)))
	}
	for i := 0; i < 12; i++ {
		out = append(out, mbMail(fmt.Sprintf("late-%d", i), "inbox-id", windowUntil.Add(time.Duration(i+1)*24*time.Hour)))
	}
	out = append(out, mbMail("early-0", "inbox-id", windowSince.Add(-time.Hour)), mbMail("early-1", "sent-id", windowSince.Add(-48*time.Hour)))
	return out
}

func runWindow(t *testing.T, db, account, coverage string) (WorkIQReport, error) {
	t.Helper()
	return RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account,
		Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil, Coverage: coverage}})
}

func coded(err error) string {
	var c *errs.Coded
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}

func TestWorkIQMailWindowProvesAWeekAndAdmitsOnlyItsMail(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	report, err := runWindow(t, db, account, "")
	if err != nil {
		t.Fatal(err)
	}
	w := report.Window
	if w == nil || !w.Covered || len(w.Folders) != 7 || report.Resumable || len(report.Accounts) != 1 || report.Accounts[0].Status != "ok" {
		t.Fatalf("a quiet week was not proven: %+v %+v", w, report.Accounts)
	}
	for _, f := range w.Folders {
		if f.Mode != "delta" || !f.Covered {
			t.Fatalf("folder %+v was not proven by delta", f)
		}
	}
	c := report.Counts
	if c["window_admitted"] != 5 || c["window_rounds"] != 1 || c["window_pages_cleared"] < 2 || c["window_deferred"] != 0 {
		t.Fatalf("counts = %v", c)
	}
	if count(t, db, `select count(*) from mail_messages m join workiq_rows w on w.kind='mail' and w.item_rowid=m.rowid
where w.identity_unproven=1 and w.winner='workiq_server' and m.received_at>='2026-09-01' and m.received_at<'2026-09-08'`) != 5 ||
		count(t, db, `select count(*) from mail_messages`) != 5 {
		t.Fatal("the window admitted mail outside its range, or not all of its own")
	}
	if count(t, db, `select count(*) from workiq_window_coverage where folder_id=''`) != 1 ||
		count(t, db, `select count(*) from workiq_window_coverage`) != 8 {
		t.Fatal("the window's coverage was not recorded per folder and for all folders")
	}
	if count(t, db, `select count(*) from sync_runs where source like 'workiq:%'`) != 0 || count(t, db, `select count(*) from workiq_censuses`) != 0 ||
		count(t, db, `select count(*) from workiq_mail_receipts`) != 0 || count(t, db, `select count(*) from workiq_accounts where inventory_revision<>0`) != 0 ||
		count(t, db, `select count(*) from workiq_attempts where kind='mail_window' and status='ok'`) != 1 ||
		count(t, db, `select count(*) from workiq_frontiers where scope<>'' and scope not like 'window:%'`) != 0 {
		t.Fatal("a window wrote whole-account evidence or health")
	}
	if count(t, db, `select count(*) from workiq_pages where data_json<>''`) != 0 {
		t.Fatal("a page body stayed after every in-window item on it was admitted")
	}
	// The same window again follows its delta links only and proves it again.
	again, err := runWindow(t, db, account, "")
	if err != nil || !again.Window.Covered || again.Counts["window_admitted"] != 0 || again.Counts["pages"] > 7*2+7+6+2*7 {
		t.Fatalf("a repeated window did not prove itself from its kept positions: %v %v", again.Counts, err)
	}
}

func TestWorkIQMailWindowRoundsRestartOnlyForChangesThatAffectIt(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	late := windowUntil.Add(30 * 24 * time.Hour)
	// Mail fetches 1-22 list the tree and count, 23-30 walk the seven folders, and 31-37 are the
	// first round in tree order (inbox first, the child folder last). Before that round, later
	// mail arrives and a later item is removed: neither can change the window.
	events := []mbEvent{
		{At: 30, Op: "add", mbMessage: mbMail("newer", "inbox-id", late)},
		{At: 30, Op: "remove", mbMessage: mbMessage{ID: "late-3"}},
	}
	writeMailbox(t, mbScenario{Messages: windowMessages(), Events: events})
	report, err := runWindow(t, db, account, "")
	if err != nil || !report.Window.Covered || report.Counts["window_rounds"] != 1 {
		t.Fatalf("out-of-window churn restarted a round: %v %v", report.Counts, err)
	}
	db, account = workIQAcquisitionArchive(t)
	// After the inbox's first follow, a window item moves from the child folder into the inbox
	// under a new ID: the child's follow reports its removal, the inbox's next follow its arrival.
	events = []mbEvent{{At: 33, Op: "move", mbMessage: mbMessage{ID: "child-0"}, To: "inbox-id", NewID: "moved-0"}}
	writeMailbox(t, mbScenario{Messages: windowMessages(), Events: events})
	report, err = runWindow(t, db, account, "")
	if err != nil || !report.Window.Covered || report.Counts["window_rounds"] != 3 {
		t.Fatalf("a move inside the window did not restart a round: %v %v", report.Counts, err)
	}
	if count(t, db, `select count(*) from workiq_rows where graph_id='moved-0'`) != 1 || count(t, db, `select count(*) from workiq_rows where graph_id='child-0'`) != 0 ||
		count(t, db, `select count(*) from mail_messages`) != 5 {
		t.Fatal("the moved item was admitted under its old ID or twice")
	}
}

func TestWorkIQMailWindowStopsResumablyWhenItKeepsChanging(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	var events []mbEvent
	for i := 0; i < 400; i++ {
		events = append(events, mbEvent{At: 31 + i, Op: "add", mbMessage: mbMail(fmt.Sprintf("busy-%d", i), "inbox-id", windowSince.Add(time.Duration(i)*time.Minute))})
	}
	writeMailbox(t, mbScenario{Messages: windowMessages(), Events: events[:40]})
	report, err := runWindow(t, db, account, "")
	if coded(err) != "workiq_window_unstable" || !report.Resumable || report.Window.Covered ||
		count(t, db, `select count(*) from workiq_window_coverage`) != 0 || count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatalf("a window that kept changing was proven or admitted, or not resumable: %v %v", report.Counts, err)
	}
	report, err = runWindow(t, db, account, "")
	if err != nil || !report.Window.Covered || count(t, db, `select count(*) from mail_messages`) != 45 {
		t.Fatalf("the next run did not prove the settled window: %v %v", report.Counts, err)
	}
}

func TestWorkIQMailWindowListingFetchesButNeverProves(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	messages := windowMessages()
	for i := 0; i < 1001; i++ {
		messages = append(messages, mbMail(fmt.Sprintf("bulk-%d", i), "child-id", windowUntil.Add(time.Duration(i+1)*time.Hour)))
	}
	writeMailbox(t, mbScenario{Messages: messages})
	report, err := runWindow(t, db, account, "")
	if err != nil || report.Window.Covered || report.Gaps["workiq_window_unproven"] != 1 || report.Accounts[0].Status != "ok_with_omissions" ||
		count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from workiq_window_coverage where folder_id=''`) != 0 ||
		count(t, db, `select count(*) from workiq_window_coverage where folder_id='child-id'`) != 0 {
		t.Fatalf("a listing folder was proven, or its mail not fetched: %v %+v %v", report.Gaps, report.Window, err)
	}
	for _, f := range report.Window.Folders {
		if (f.FolderID == "child-id") != (f.Mode == "listing") || f.FolderID == "child-id" && (f.ItemsOpen != 1003 || f.ItemsInWindow != 2) {
			t.Fatalf("folder choice %+v", f)
		}
	}
	// A listing finished by one run is walked again by the next.
	again, err := runWindow(t, db, account, "fast")
	if err != nil || again.Window.Covered || again.Gaps["workiq_window_unproven"] != 7 || count(t, db, `select count(*) from mail_messages`) != 5 {
		t.Fatalf("a repeated listing failed or proved: %v %v", again.Gaps, err)
	}
	proof, err := runWindow(t, db, account, "proof")
	if err != nil || !proof.Window.Covered {
		t.Fatalf("proof mode did not prove the window: %v", err)
	}
}

func TestWorkIQMailWindowDiskChecks(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	// Nothing is staged yet: admitting the window's own items still has to fit.
	floor := int64(2*16<<20 + 256<<20 + 1<<30)
	setWorkIQFreeBytes(t, func(string) (int64, error) { return floor + 5*store.WorkIQAdmitBytesPerItem, nil })
	report, err := runWindow(t, db, account, "")
	if coded(err) != "workiq_disk_space" || report.Resumable || count(t, db, `select count(*) from workiq_pages`) != 0 ||
		report.Counts["disk_needed_bytes"] <= report.Counts["disk_free_bytes"] || !strings.Contains(err.Error(), "workiq_disk_space") {
		t.Fatalf("a window that cannot be admitted was not refused before its first page: %v %v", report.Counts, err)
	}
	var coded2 *errs.Coded
	if !errors.As(err, &coded2) || !strings.Contains(coded2.Fix, "shorter date range") || coded2.Exit != errs.ExitEnvironment {
		t.Fatal("the refusal's fix does not name the shorter range")
	}
	calls := 0
	setWorkIQFreeBytes(t, func(string) (int64, error) {
		calls++
		if calls > 3 {
			return floor, nil
		}
		return 1 << 40, nil
	})
	report, err = runWindow(t, db, account, "")
	if coded(err) != "workiq_disk_reserve" || !report.Resumable || count(t, db, `select count(*) from workiq_pages`) == 0 {
		t.Fatalf("free space falling mid-walk did not stop resumably: %v %v", report.Counts, err)
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 1 << 40, nil })
	if report, err = runWindow(t, db, account, ""); err != nil || !report.Window.Covered {
		t.Fatalf("the next run did not continue: %v", err)
	}
	// Free space falling just before admission stops with nothing admitted.
	db, account = workIQAcquisitionArchive(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	setWorkIQFreeBytes(t, func(string) (int64, error) {
		if count(t, db, `select count(*) from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where f.resource like '%deltatoken%' or p.expected_revision>0`) > 0 &&
			count(t, db, `select coalesce(max(revision),0) from workiq_frontiers`) >= 3 {
			return floor, nil
		}
		return 1 << 40, nil
	})
	report, err = runWindow(t, db, account, "")
	if coded(err) != "workiq_disk_reserve" || !report.Resumable || count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatalf("a short disk at admission did not stop before admitting: %v %v", report.Counts, err)
	}
}

func TestWorkIQMailWindowKeepsItsPositionAtThePageLimit(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	old := workIQMailPageLimit
	t.Cleanup(func() { workIQMailPageLimit = old })
	workIQMailPageLimit = 30
	report, err := runWindow(t, db, account, "")
	if coded(err) != "workiq_page_limit" || !report.Resumable || report.Window.Covered {
		t.Fatalf("a page budget stop was not resumable: %v", err)
	}
	workIQMailPageLimit = old
	if report, err = runWindow(t, db, account, ""); err != nil || !report.Window.Covered || count(t, db, `select count(*) from mail_messages`) != 5 {
		t.Fatalf("the next run did not finish from its kept position: %v", err)
	}
}

func TestWorkIQMailWindowRestartsAFolderWhoseDeltaExpired(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages(), Expire: []map[string]any{{"folder": "inbox-id", "at": 30}}})
	report, err := runWindow(t, db, account, "")
	if err != nil || !report.Window.Covered || count(t, db, `select count(*) from workiq_frontiers where retired=1`) != 1 ||
		count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from workiq_pages where data_json<>''`) != 0 {
		t.Fatalf("an expired delta link was not restarted: %v %v", report.Counts, err)
	}
}

func TestWorkIQMailWindowRefusesAChangedFolderTree(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages(), Events: []mbEvent{{At: 40, Op: "add_folder", mbMessage: mbMessage{ID: "new-folder"}}}})
	report, err := runWindow(t, db, account, "")
	if coded(err) != "workiq_window_unstable" || !report.Resumable || count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatalf("a changed folder tree was proven: %v", err)
	}
	if report, err = runWindow(t, db, account, ""); err != nil || !report.Window.Covered || len(report.Window.Folders) != 8 {
		t.Fatalf("the next run did not walk the new folder: %v", err)
	}
}

func TestWorkIQMailWindowDefersMailTheArchiveAlreadyHolds(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cached := windowSince.Add(24 * time.Hour)
	m := cachedMail(account, 9, "<in-0@example.test>", cached)
	if _, err := s.CommitMail(ctx, nativeBatch(account, cached, m)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	report, err := runWindow(t, db, account, "")
	if err != nil || !report.Window.Covered || report.Counts["window_deferred"] != 1 || report.Counts["window_admitted"] != 4 ||
		count(t, db, `select count(*) from mail_messages`) != 5 {
		t.Fatalf("mail already in the archive was not deferred: %v %v", report.Counts, err)
	}
}

func TestWorkIQMailWindowOptionsAreValidated(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	ctx := context.Background()
	for name, options := range map[string]WorkIQOptions{
		"chats":          {DBPath: db, Source: "chats", Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil}},
		"teams account":  {DBPath: db, Account: "tenant/user", Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil}},
		"empty range":    {DBPath: db, Source: "mail", Window: &WorkIQMailWindow{Since: windowUntil, Until: windowUntil}},
		"future end":     {DBPath: db, Source: "mail", Window: &WorkIQMailWindow{Since: windowSince, Until: time.Now().Add(time.Hour)}},
		"no start":       {DBPath: db, Source: "mail", Window: &WorkIQMailWindow{Until: windowUntil}},
		"bad coverage":   {DBPath: db, Source: "mail", Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil, Coverage: "maybe"}},
		"coverage probe": {DBPath: db, Source: "mail", Kind: "coverage", Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil}},
	} {
		if _, err := RunWorkIQ(ctx, options); coded(err) != errs.CodeUsage {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// An Outlook account with source all narrows to the mail window.
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Account: account, Window: &WorkIQMailWindow{Since: windowSince, Until: windowUntil}})
	if err != nil || report.Window == nil || !report.Window.Covered {
		t.Fatalf("an Outlook account did not select the mail window: %v", err)
	}
}

// cachedMail is a native Outlook message matching the scenario's own copy of the same message.
func cachedMail(account string, key uint32, header string, at time.Time) outlookmail.Message {
	m := outlookmail.Message{Account: account, Folder: outlookmail.Folder{Key: 1, Kind: "inbox"}, Copies: 1}
	m.DetailKey, m.FolderKey = key, 1
	m.MessageID, m.Subject, m.Received, m.Sent = header, "Synthetic in-0", at, at
	m.SenderName, m.SenderAddress = "Ann Sender", "ann@example.test"
	m.Class, m.ReadState = "IPM.Note", "unknown"
	return m
}

func nativeBatch(account string, at time.Time, messages ...outlookmail.Message) store.MailBatch {
	return store.MailBatch{Account: account, ReadAt: at, FreshAt: at, Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Native inbox", Kind: "inbox"}}, Messages: messages,
			Coverage: []outlookmail.Coverage{{FolderKey: 1, Oldest: at, Newest: at, Count: len(messages)}}}}
}
