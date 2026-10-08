//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// The Outlook mail real-store checks (plan-mail.md, Task 6). They read the store under
// M365CRAWL_OUTLOOK_ROOT (<root>/<profile>/HxStore.hxd) and skip without M365CRAWL_REAL_CACHE=1 and
// that root. They log counts, durations and sizes only: never a subject, address, folder name,
// attachment name or path. The store is only opened through a private copy (outlookdesktop.Snapshot,
// and the sync's own copy); the profile directory is read only to stat attachment files.
//
// Fail: a read that is not trusted (any loss), a sync that is not ok, a marked-absent, gone or
// evicted message on a clean read, folder links, field coverage, attachment sizes, the count
// sanity ranges and the two wall-time budgets. Report: every other count.

// Sanity ranges come from a real store read on 2026-10-07 (plan-mail.md A23): 1,704 messages,
// 25 folders, and header/detail/body/attachment/folder/recipient objects of 3,207 / 7,010 / 3,830 /
// 6,927 / 1,714 / 68,108. The bands are wide (about half to three times) so a mailbox that grows
// stays inside, while a mapper that reads nothing, or the wrong class, falls outside.
var mailThresholds = struct {
	Messages, Folders                                 [2]int
	Headers, Details, Bodies, Attachments, FolderObjs [2]int
	Recipients                                        [2]int
	MessagesVsDetailKeysTol                           float64 // messages within this fraction of the distinct detail keys
	FieldCoverageMin                                  float64 // subject, sender and received time
	ColdWallMax, IncrementalWallMax                   time.Duration
}{
	Messages: [2]int{800, 6000}, Folders: [2]int{8, 100},
	Headers: [2]int{1500, 10000}, Details: [2]int{3000, 25000}, Bodies: [2]int{1800, 13000},
	Attachments: [2]int{3000, 25000}, FolderObjs: [2]int{800, 6000}, Recipients: [2]int{34000, 220000},
	MessagesVsDetailKeysTol: 0.05,
	FieldCoverageMin:        0.95,
	ColdWallMax:             60 * time.Second, IncrementalWallMax: 10 * time.Second,
}

// mailRunData is the shared mail syncs: a cold Outlook-only sync into a fresh archive, a sync of the
// unchanged store, and a forced re-read of the populated archive (the incremental sync).
type mailRunData struct {
	profile                outlookdesktop.Profile
	db                     string
	cold, unchanged, again syncMeasure
}

var (
	mailOnce sync.Once
	mailRun  mailRunData
	mailErr  string
)

func requireMail(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Outlook mail is not read on Windows")
	}
	return requireOutlook(t)
}

// mailSyncs runs the mail syncs once. It does nothing else first, so the peak RSS they record is
// not raised by a check that scanned the store.
func mailSyncs(t *testing.T) mailRunData {
	t.Helper()
	root := requireMail(t)
	mailOnce.Do(func() {
		finished := false
		defer func() {
			if !finished && mailErr == "" {
				mailErr = "the shared mail syncs did not finish (see the first failing mail check)"
			}
		}()
		d := &mailRun
		d.profile = outlookStore(t, root)
		dir := calendarScratch(t)
		d.db = filepath.Join(dir, "mail.db")
		opts := syncer.Options{
			Root: filepath.Join(dir, "no-teams"), DBPath: d.db,
			OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
		}
		if d.cold, mailErr = measure(opts); mailErr != "" {
			return
		}
		if d.unchanged, mailErr = measure(opts); mailErr != "" {
			return
		}
		forced := opts
		forced.FullRead = true
		if d.again, mailErr = measure(forced); mailErr != "" {
			return
		}
		finished = true
	})
	if mailErr != "" {
		t.Fatal(mailErr)
	}
	return mailRun
}

// mailSource is the mail source row of a report, and whether the report had one.
func mailSource(rep syncer.Report) (syncer.SourceReport, bool) {
	for _, s := range rep.Sources {
		if strings.HasSuffix(s.Source, "|mail") {
			return s, true
		}
	}
	return syncer.SourceReport{}, false
}

// mustMail is the mail source row of a report, empty when it has none.
func mustMail(rep syncer.Report) syncer.SourceReport {
	s, _ := mailSource(rep)
	return s
}

func mib(b int64) float64 { return float64(b) / (1 << 20) }

// (1) The syncs: trusted reads, no losses, nothing marked gone, and the wall-time budgets.
func TestRealMailSync(t *testing.T) {
	d := mailSyncs(t)
	c := mailThresholds
	for _, step := range []struct {
		name string
		m    syncMeasure
	}{{"cold", d.cold}, {"unchanged", d.unchanged}, {"incremental (forced re-read)", d.again}} {
		src, ok := mailSource(step.m.rep)
		if !ok {
			t.Errorf("%s: the report has no mail source", step.name)
			continue
		}
		mc := mailCountsOf(src)
		t.Logf("%s: wall %s, mail source status %s, omissions %s; added=%d updated=%d replaced=%d gone=%d evicted=%d withheld=%d",
			step.name, step.m.wall.Round(time.Millisecond), src.Status, countsLine(src.Omissions), mc.Added, mc.Updated, mc.Replaced, mc.Gone, mc.Evicted, mc.Withheld)
		if step.name == "unchanged" {
			continue // a skipped read has no mail counts to judge
		}
		// Trusted on a live store: no losses, so the source is ok and carries no omissions.
		if src.Status != syncer.StatusOK || len(src.Omissions) != 0 {
			t.Errorf("%s: the mail read is not trusted: status %s, omissions %s (want ok and none)", step.name, src.Status, countsLine(src.Omissions))
		}
		if mc.Gone != 0 || mc.Evicted != 0 || mc.Withheld != 0 {
			t.Errorf("%s: gone=%d evicted=%d withheld=%d on a store read twice from one copy, want 0 each", step.name, mc.Gone, mc.Evicted, mc.Withheld)
		}
	}
	// The incremental read of an archive that already holds the mail must add nothing new.
	if m := mailCountsOf(mustMail(d.again.rep)); m.Added != 0 || m.Replaced != 0 {
		t.Errorf("incremental: added=%d replaced=%d on a re-read of the same copy, want 0 each", m.Added, m.Replaced)
	}

	// Two trusted reads of one copy must not leave any message marked absent (A23 rule 2, A24).
	st := openArchive(t, d.db)
	absent := asInt(rowsOf(t, st, `select count(*) from mail_absent`)[0][0])
	t.Logf("mail_absent rows after the cold and the forced read: %d", absent)
	if absent != 0 {
		t.Errorf("%d messages are marked absent after two reads of one unchanged store, want 0", absent)
	}

	// Budgets (plan: cold under 60 s, incremental under 10 s on this Mac).
	if d.cold.wall > c.ColdWallMax {
		t.Errorf("the cold sync took %s, over %s", d.cold.wall.Round(time.Millisecond), c.ColdWallMax)
	}
	if d.again.wall > c.IncrementalWallMax {
		t.Errorf("the incremental sync took %s, over %s", d.again.wall.Round(time.Millisecond), c.IncrementalWallMax)
	}
	if d.unchanged.wall > c.IncrementalWallMax {
		t.Errorf("the unchanged sync took %s, over %s", d.unchanged.wall.Round(time.Millisecond), c.IncrementalWallMax)
	}
	if d.again.peakAfter > 0 {
		t.Logf("peak RSS of this process after the mail syncs: %.1f MiB (after cold %.1f MiB)", mib(d.again.peakAfter), mib(d.cold.peakAfter))
	} else {
		t.Log("peak RSS: not available on this platform")
	}
}

// mailCountsOf is the mail commit counts of a source row, zero when it has none.
func mailCountsOf(s syncer.SourceReport) store.MailResult {
	if s.Counts == nil || s.Counts.Mail == nil {
		return store.MailResult{}
	}
	return *s.Counts.Mail
}

// scannedMail is one private copy of the store read once with outlookmail.Collect (bodies left
// unread, as in the 2026-10-07 count), shared by the read check and the attachment check.
type scannedMail struct {
	info outlookdesktop.Info
	wall time.Duration
	res  outlookmail.Result
	err  string
}

var mailScanOnce struct {
	done bool
	s    scannedMail
}

func scanMail(t *testing.T, d mailRunData) scannedMail {
	t.Helper()
	if mailScanOnce.done {
		return mailScanOnce.s
	}
	mailScanOnce.done = true
	s := &mailScanOnce.s
	info, cleanup, err := outlookdesktop.Snapshot(context.Background(), d.profile.StorePath)
	calMu.Lock()
	calCleanups = append(calCleanups, cleanup)
	calMu.Unlock()
	if err != nil {
		s.err = "copy failed with code " + errCode(err)
		return *s
	}
	s.info = info
	f, err := os.Open(info.Path)
	if err != nil {
		s.err = "open of the copy failed"
		return *s
	}
	defer func() { _ = f.Close() }()
	start := time.Now()
	hs, err := hxstore.OpenStore(f, info.Size)
	if err == nil {
		s.res, err = outlookmail.Collect(context.Background(), hs, d.profile.Dir, "acceptance", outlookmail.Options{})
	}
	s.wall = time.Since(start)
	if err != nil {
		var g *hxstore.GuardError
		if errors.As(err, &g) {
			s.err = "the guard refused the store: " + g.Code
		} else {
			s.err = "scan failed"
		}
	}
	return *s
}

func inRange(n int, r [2]int) bool { return n >= r[0] && n <= r[1] }

// (2) The read itself: container counts, trust, notes and the logical messages.
func TestRealMailRead(t *testing.T) {
	d := mailSyncs(t)
	s := scanMail(t, d)
	if s.err != "" {
		t.Fatal(s.err)
	}
	c, res, n := mailThresholds, s.res, s.res.Notes
	perClass := map[uint16]int{}
	for p, k := range res.Stats.Pairs {
		perClass[p.Class] += k
	}
	resynced := map[uint16]int{}
	for p, k := range res.Stats.PairsResynced {
		resynced[p.Class] += k
	}
	for _, e := range []struct {
		name  string
		class uint16
		r     [2]int
	}{
		{"header", outlookmail.ClassHeader, c.Headers}, {"detail", outlookmail.ClassDetail, c.Details},
		{"body", outlookmail.ClassBody, c.Bodies}, {"attachment", outlookmail.ClassAttachment, c.Attachments},
		{"folder", outlookmail.ClassFolder, c.FolderObjs}, {"recipient", outlookmail.ClassRecipient, c.Recipients},
	} {
		got := perClass[e.class]
		t.Logf("class 0x%x (%s): %d objects, %d resynced (sanity range %d to %d)", e.class, e.name, got, resynced[e.class], e.r[0], e.r[1])
		if !inRange(got, e.r) {
			t.Errorf("class 0x%x (%s) has %d objects, outside the sanity range %d to %d", e.class, e.name, got, e.r[0], e.r[1])
		}
	}

	t.Logf("scan wall %s; messages=%d folders=%d seen detail keys=%d", s.wall.Round(time.Millisecond), len(res.Messages), len(res.Folders), len(res.SeenDetailKeys))
	t.Logf("notes: headers seen=%d missing detail=%d missing folder=%d other root=%d orphan attachments=%d orphan recipients=%d bad strings=%d unmapped=%d resynced skipped=%d other tag skipped=%d",
		n.HeadersSeen, n.MissingDetail, n.MissingFolder, n.OtherRoot, n.OrphanAttachments, n.OrphanRecipients, n.BadString, n.Unmapped, n.ResyncedSkipped, n.OtherTagSkipped)
	t.Logf("bodies (not read from files here): inline=%d file read=%d none=%d not read=%d missing=%d unreadable=%d",
		n.BodiesInline, n.BodiesFile, n.BodiesNone, n.BodiesNotRead, n.BodiesMissing, n.BodiesUnreadable)
	losses := map[string]int{}
	for _, l := range res.Losses {
		losses[l.Code] += l.Count
	}
	t.Logf("losses: %s; doubtful=%v", countsLine(losses), res.Doubtful)

	if len(res.Losses) != 0 || res.Doubtful {
		t.Errorf("the read is not trusted: losses %s, doubtful=%v (want none)", countsLine(losses), res.Doubtful)
	}
	// Every header maps or is counted in the notes: nothing unmapped, and what the notes explain adds up.
	if n.Unmapped != 0 {
		t.Errorf("%d objects failed to map, want 0", n.Unmapped)
	}
	if len(res.SeenDetailKeys) < len(res.Messages) {
		t.Errorf("%d messages but only %d seen detail keys", len(res.Messages), len(res.SeenDetailKeys))
	}
	if !inRange(len(res.Messages), c.Messages) {
		t.Errorf("%d messages is outside the sanity range %d to %d", len(res.Messages), c.Messages[0], c.Messages[1])
	}
	if !inRange(len(res.Folders), c.Folders) {
		t.Errorf("%d folders is outside the sanity range %d to %d", len(res.Folders), c.Folders[0], c.Folders[1])
	}
	if !within(len(res.Messages), len(res.SeenDetailKeys), c.MessagesVsDetailKeysTol) {
		t.Errorf("%d logical messages is not within %.0f%% of the %d distinct detail keys", len(res.Messages), 100*c.MessagesVsDetailKeysTol, len(res.SeenDetailKeys))
	}

	// Every message names a folder of the read, and a downloaded attachment's size is the file's.
	folders := map[uint32]bool{}
	for _, f := range res.Folders {
		folders[f.Key] = true
	}
	badFolder, atts, recips, downloaded, noPath, noFile, sizeBad, sized := 0, 0, 0, 0, 0, 0, 0, 0
	for _, m := range res.Messages {
		if !folders[m.Folder.Key] {
			badFolder++
		}
		recips += len(m.Recipients)
		for _, a := range m.Attachments {
			atts++
			if !a.Downloaded {
				continue
			}
			downloaded++
			if a.Path == "" {
				noPath++
				continue
			}
			fi, ok := statProfileFile(d.profile.Dir, a.Path)
			if !ok {
				noFile++
				continue
			}
			sized++
			if fi != a.Size {
				sizeBad++
			}
		}
	}
	t.Logf("recipients=%d attachments=%d downloaded=%d (no path=%d, file not found=%d, compared=%d, size differs=%d)", recips, atts, downloaded, noPath, noFile, sized, sizeBad)
	if badFolder != 0 {
		t.Errorf("%d messages name a folder that is not in the read, want 0", badFolder)
	}
	if sizeBad != 0 {
		t.Errorf("%d downloaded attachments differ in size from their file, want 0", sizeBad)
	}
}

// statProfileFile is the size of the file a "~/Files/..." attachment path names under the profile
// directory, and whether it is a plain file there. It stats only; a path that is absolute, has a
// backslash or a ".." element, or does not start with "~/Files/" is not followed.
func statProfileFile(profileDir, p string) (int64, bool) {
	rel, ok := strings.CutPrefix(p, "~/")
	if !ok || !strings.HasPrefix(rel, "Files/") || strings.ContainsRune(rel, '\\') || filepath.IsAbs(rel) {
		return 0, false
	}
	for _, el := range strings.Split(rel, "/") {
		if el == ".." || el == "" {
			return 0, false
		}
	}
	fi, err := os.Stat(filepath.Join(profileDir, filepath.FromSlash(rel)))
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// (3) The archive after the cold sync: field coverage, folder links and counts.
func TestRealMailArchive(t *testing.T) {
	d := mailSyncs(t)
	st := openArchive(t, d.db)
	count := func(q string) int {
		t.Helper()
		return asInt(rowsOf(t, st, q)[0][0])
	}
	msgs := count(`select count(*) from mail_messages`)
	folders := count(`select count(*) from mail_folders`)
	recips := count(`select count(*) from mail_recipients`)
	atts := count(`select count(*) from mail_attachments`)
	t.Logf("archive: messages=%d folders=%d recipients=%d attachments=%d (downloaded %d)", msgs, folders, recips, atts, count(`select count(*) from mail_attachments where downloaded=1`))

	bodies := map[string]int{}
	for _, r := range rowsOf(t, st, `select body_state, count(*) from mail_messages group by body_state`) {
		bodies[asString(r[0])] = asInt(r[1])
	}
	t.Logf("bodies by state: %s", countsLine(bodies))

	if msgs == 0 {
		t.Fatal("the archive holds no mail after the cold sync")
	}
	c := mailThresholds
	if !inRange(msgs, c.Messages) {
		t.Errorf("%d messages is outside the sanity range %d to %d", msgs, c.Messages[0], c.Messages[1])
	}
	if !inRange(folders, c.Folders) {
		t.Errorf("%d folders is outside the sanity range %d to %d", folders, c.Folders[0], c.Folders[1])
	}

	withSubject := count(`select count(*) from mail_messages where subject <> ''`)
	withSender := count(`select count(*) from mail_messages where sender_name <> '' or sender_address <> ''`)
	withReceived := count(`select count(*) from mail_messages where received_at is not null and received_at <> ''`)
	complete := count(`select count(*) from mail_messages where subject <> '' and (sender_name <> '' or sender_address <> '') and received_at is not null and received_at <> ''`)
	t.Logf("field coverage: subject %.1f%% sender %.1f%% received %.1f%% all three %.1f%% (want %.0f%%)",
		100*ratio(withSubject, msgs), 100*ratio(withSender, msgs), 100*ratio(withReceived, msgs), 100*ratio(complete, msgs), 100*c.FieldCoverageMin)
	if r := ratio(complete, msgs); r < c.FieldCoverageMin {
		t.Errorf("%.1f%% of messages have a subject, a sender and a received time, want at least %.0f%%", 100*r, 100*c.FieldCoverageMin)
	}

	dangling := count(`select count(*) from mail_messages m where not exists (select 1 from mail_folders f where f.account=m.account and f.folder_key=m.folder_key)`)
	t.Logf("messages whose folder link does not resolve: %d", dangling)
	if dangling != 0 {
		t.Errorf("%d messages have a folder link that does not resolve, want 0", dangling)
	}
	orphanRecips := count(`select count(*) from mail_recipients r where not exists (select 1 from mail_messages m where m.rowid=r.message_rowid)`)
	orphanAtts := count(`select count(*) from mail_attachments a where not exists (select 1 from mail_messages m where m.rowid=a.message_rowid)`)
	t.Logf("recipient rows without a message: %d; attachment rows without a message: %d", orphanRecips, orphanAtts)
	if orphanRecips != 0 || orphanAtts != 0 {
		t.Errorf("recipient and attachment rows without a message: %d and %d, want 0", orphanRecips, orphanAtts)
	}
	if recips == 0 {
		t.Error("no recipient rows were stored")
	}
}
