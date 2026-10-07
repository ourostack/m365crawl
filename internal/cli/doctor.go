package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const staleSyncAfter = 24 * time.Hour

var (
	openArchiveReadOnly = store.OpenReadOnly
	readArchiveStatus   = func(st *store.Store, ctx context.Context) (store.StatusRow, error) { return st.Status(ctx) }
	readCalendarCache   = func(st *store.Store, ctx context.Context) (store.CalendarCache, error) { return st.CalendarCache(ctx) }
	readCalendarSources = func(st *store.Store, ctx context.Context, f store.CalendarSourcesFilter) (store.CalendarSources, error) {
		return st.CalendarSources(ctx, f)
	}
	outlookDefaultRoot  = outlookdesktop.DefaultRoot
	outlookDiscover     = outlookdesktop.Discover
	outlookOpenStore    = hxstore.OpenFile
	needsArchiveUpgrade = func(st *store.Store, ctx context.Context) (bool, error) { return st.NeedsUpgrade(ctx) }
)

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

type doctorResult struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`

	snap *render.Snapshot // text mode only; never serialized
}

type doctorCmd struct{}

func (doctorCmd) Run(rt *runtime) error {
	res := &doctorResult{OK: true, Checks: rt.doctorChecks()}
	var failed []string
	for _, c := range res.Checks {
		if !c.OK {
			res.OK = false
			failed = append(failed, c.Name)
		}
	}
	if rt.format == output.Text {
		res.snap = rt.doctorSnapshot()
	}
	if err := rt.write("doctor", res); err != nil {
		return err
	}
	if !res.OK {
		return errs.DoctorFailed("failing checks: " + strings.Join(failed, ", "))
	}
	return nil
}

func (rt *runtime) doctorChecks() []check {
	root := rt.root
	if root == "" {
		root = teamsdesktop.DefaultRoot()
	}
	sources, other, derr := discover(root)
	var coded *errs.Coded
	errors.As(derr, &coded)
	code := ""
	if coded != nil {
		code = coded.Code
	}

	var cs []check
	// teams_installed
	switch code {
	case errs.CodeTeamsNotInstalled:
		cs = append(cs, check{Name: "teams_installed", Detail: coded.Message, Fix: coded.Fix})
	default:
		cs = append(cs, check{Name: "teams_installed", OK: true, Detail: "Teams data found at " + root})
	}
	// full_disk_access
	cs = append(cs, fullDiskAccessDoctorCheck(code, coded))
	// teams_origin
	cs = append(cs, teamsOriginDoctorCheck(sources, other, derr, code, coded))
	return append(cs, rt.archiveChecks()...)
}

func (rt *runtime) archiveChecks() []check {
	var cs []check
	cs = append(cs, rt.writableCheck())
	st, err := openArchiveReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		const d = "no archive yet; the first sync creates it"
		return append(cs,
			check{Name: "schema_version", OK: true, Detail: d},
			check{Name: "fts", OK: true, Detail: d},
			check{Name: "last_sync_age", OK: true, Warn: true, Detail: "never synced", Fix: "Run `m365crawl sync`."},
			check{Name: "calendar_cache", OK: true, Detail: d},
			rt.outlookStoreCheck(nil))
	case isArchiveNewer(err):
		// A newer schema is refused at open, so no other check can read the archive.
		var coded *errs.Coded
		_ = errors.As(err, &coded)
		return append(cs,
			check{Name: "schema_version", Detail: coded.Message, Fix: coded.Fix},
			check{Name: "fts", Detail: "cannot read an archive written by a newer m365crawl", Fix: coded.Fix},
			check{Name: "last_sync_age", Detail: "cannot read an archive written by a newer m365crawl", Fix: coded.Fix})
	case err != nil:
		fix := "Check that " + rt.dbPath + " is a m365crawl archive; move it aside and run `m365crawl sync` to rebuild it."
		return append(cs,
			check{Name: "schema_version", Detail: "cannot open the archive: " + err.Error(), Fix: fix},
			check{Name: "fts", Detail: "cannot open the archive", Fix: fix},
			check{Name: "last_sync_age", Detail: "cannot open the archive", Fix: fix})
	}
	defer func() { _ = st.Close() }()
	row, err := readArchiveStatus(st, rt.ctx)
	if err != nil {
		fix := "Run `m365crawl sync`; if it fails, move " + rt.dbPath + " aside and sync again."
		return append(cs, check{Name: "schema_version", Detail: "cannot read the archive: " + err.Error(), Fix: fix},
			check{Name: "fts", Detail: "cannot read the archive", Fix: fix}, check{Name: "last_sync_age", Detail: "cannot read the archive", Fix: fix})
	}
	if row.SchemaVersion > store.SchemaVersion {
		e := errs.ArchiveSchemaNewer(row.SchemaVersion, store.SchemaVersion)
		return append(cs,
			check{Name: "schema_version", Detail: e.Message, Fix: e.Fix},
			check{Name: "fts", Detail: "cannot read an archive written by a newer m365crawl", Fix: e.Fix},
			check{Name: "last_sync_age", Detail: "cannot read an archive written by a newer m365crawl", Fix: e.Fix})
	}
	if row.SchemaVersion == store.SchemaVersion {
		cs = append(cs, check{Name: "schema_version", OK: true, Detail: fmt.Sprintf("schema v%d", row.SchemaVersion)})
	} else { // older: a newer archive is refused when it is opened
		cs = append(cs, check{Name: "schema_version", Detail: fmt.Sprintf("archive is schema v%d, expected v%d", row.SchemaVersion, store.SchemaVersion), Fix: "Run `m365crawl sync` to migrate the archive."})
	}
	if row.FTSPresent {
		cs = append(cs, check{Name: "fts", OK: true, Detail: "full-text indexes present"})
	} else {
		cs = append(cs, check{Name: "fts", Detail: "full-text indexes are missing", Fix: "Run `m365crawl sync`; if they stay missing, move " + rt.dbPath + " aside and sync again."})
	}
	cs = append(cs, rt.archiveNewerCheck(st))
	// Status just read the archive, so the probe cannot fail here; a failure would only hide the warning.
	old, _ := needsArchiveUpgrade(st, rt.ctx)
	if old {
		cs = append(cs, check{Name: "archive_upgrade", OK: true, Warn: true, Detail: "archive from an older version; the next sync upgrades it", Fix: "Run `m365crawl sync`."})
	}
	if c, ok := lastSyncStatusCheck(row.LastRun); ok {
		cs = append(cs, c)
	}
	switch {
	case old: // the archive_upgrade warning already says to sync
		cs = append(cs, check{Name: "last_sync_age", OK: true, Detail: "no per-account sync record yet (archive from an older version)"})
	case row.LastSuccessAt.IsZero():
		cs = append(cs, check{Name: "last_sync_age", OK: true, Warn: true, Detail: "no successful sync yet", Fix: "Run `m365crawl sync`."})
	case rt.now().Sub(row.LastSuccessAt) > staleSyncAfter:
		cs = append(cs, check{Name: "last_sync_age", OK: true, Warn: true, Detail: "last successful sync " + rt.now().Sub(row.LastSuccessAt).Round(time.Minute).String() + " ago", Fix: "Run `m365crawl sync`."})
	default:
		cs = append(cs, check{Name: "last_sync_age", OK: true, Detail: "last successful sync " + rt.now().Sub(row.LastSuccessAt).Round(time.Second).String() + " ago"})
	}
	return append(cs, rt.calendarCacheCheck(st), rt.outlookStoreCheck(st))
}

// staleCalendarAfter is how old the Teams calendar cache may be before doctor warns: Teams
// refreshes it when its calendar view is open, so a week without one means the data is going stale.
const staleCalendarAfter = 7 * 24 * time.Hour

// oneUnit is d in whole hours, minutes or seconds (the largest unit that fits), as in "26h".
func oneUnit(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// calendarCacheCheck warns, never fails, when the archive's calendar is missing or old. Its detail
// says nothing about event content.
func (rt *runtime) calendarCacheCheck(st *store.Store) check {
	open := "Open the Teams calendar once so Teams caches it, then run `m365crawl sync`."
	c, err := readCalendarCache(st, rt.ctx)
	switch {
	case err != nil:
		return check{Name: "calendar_cache", OK: true, Warn: true, Detail: "cannot read the calendar state: " + err.Error(), Fix: "Run `m365crawl sync`."}
	case c.Accounts == 0:
		return check{Name: "calendar_cache", OK: true, Detail: "no accounts archived yet"}
	case c.WithoutDatabase > 0:
		return check{Name: "calendar_cache", OK: true, Warn: true, Detail: fmt.Sprintf("%d of %d accounts have no Teams calendar database in the archive", c.WithoutDatabase, c.Accounts), Fix: open}
	case !c.HasTables:
		return check{Name: "calendar_cache", OK: true, Warn: true, Detail: "the archive has no calendar tables yet", Fix: "Run `m365crawl sync`."}
	case c.FreshAt.IsZero():
		return check{Name: "calendar_cache", OK: true, Warn: true, Detail: "no calendar has been derived yet", Fix: open}
	case rt.now().Sub(c.FreshAt) > staleCalendarAfter:
		return check{Name: "calendar_cache", OK: true, Warn: true, Detail: "the Teams calendar cache was last fresh " + oneUnit(rt.now().Sub(c.FreshAt)) + " ago", Fix: open}
	}
	return check{Name: "calendar_cache", OK: true, Detail: "the Teams calendar cache was fresh " + oneUnit(rt.now().Sub(c.FreshAt)) + " ago"}
}

// isArchiveNewer reports the coded archive_newer error that opening a newer archive returns.
func isArchiveNewer(err error) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == errs.CodeArchiveNewer
}

// archiveNewerCheck fails when the archive was written by a newer build: every sync would refuse
// it (archive_newer, exit 3) while the other checks pass.
func (rt *runtime) archiveNewerCheck(st *store.Store) check {
	have, err := st.DerivationVersion(rt.ctx)
	switch {
	case err != nil:
		return check{Name: "archive_newer", Detail: "cannot read the archive's derivation version: " + err.Error(), Fix: "Run `m365crawl sync`; if it fails, move " + rt.dbPath + " aside and sync again."}
	case have > store.DerivationVersion:
		e := errs.ArchiveNewer(have, store.DerivationVersion)
		return check{Name: "archive_newer", Detail: e.Message, Fix: e.Fix}
	}
	return check{Name: "archive_newer", OK: true, Detail: fmt.Sprintf("the archive was written by this or an older m365crawl (derivation version %d)", have)}
}

// lastSyncStatusCheck warns when the last sync did not finish cleanly. A partial or failed sync is
// not a failure of the environment (it can be transient), so it never fails doctor; the fix says
// how to find the cause.
func lastSyncStatusCheck(r *store.RunRow) (check, bool) {
	if r == nil {
		return check{}, false
	}
	switch r.Status {
	case "partial":
		return check{Name: "last_sync_status", OK: true, Warn: true, Detail: "the last sync was partial: some Teams sources synced and others failed",
			Fix: "Run `m365crawl sync` to see which sources failed and why, fix them (the checks above name the usual causes) and sync again."}, true
	case "failed":
		return check{Name: "last_sync_status", OK: true, Warn: true, Detail: "the last sync failed",
			Fix: "Run `m365crawl sync` to see the error; the checks above name the usual causes."}, true
	}
	return check{Name: "last_sync_status", OK: true, Detail: "the last sync was " + r.Status}, true
}

// writableCheck proves the archive can be written without touching it: it creates and removes a
// scratch file beside it (in the nearest existing directory when the archive dir is missing).
func (rt *runtime) writableCheck() check {
	dir := filepath.Dir(rt.dbPath)
	for dir != filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	fix := "Make " + dir + " writable, or pass --db with a path you can write."
	f, err := os.CreateTemp(dir, ".m365crawl-doctor-*")
	if err != nil {
		return check{Name: "database_writable", Detail: "cannot write in " + dir + ": " + err.Error(), Fix: fix}
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	if _, err := os.Stat(rt.dbPath); err == nil {
		g, err := os.OpenFile(rt.dbPath, os.O_WRONLY, 0) //nolint:gosec // G304: the user's own archive path
		if err != nil {
			return check{Name: "database_writable", Detail: "cannot write " + rt.dbPath + ": " + err.Error(), Fix: fix}
		}
		_ = g.Close()
		return check{Name: "database_writable", OK: true, Detail: rt.dbPath + " is writable"}
	}
	return check{Name: "database_writable", OK: true, Detail: rt.dbPath + " does not exist yet; " + dir + " is writable"}
}

// outlookStoreCheck reports the Outlook source: off, or on with the profiles it finds, whether each
// store's header (a 64-byte read of the live file, never a copy) is a version this build reads, and
// what the archive remembers of the last read. It warns and never fails, because Outlook is optional
// and fails alone. Its detail holds no event content. Outlook is on by default, so a machine with no
// new Outlook, or with Outlook and nothing wrong, is a plain pass: only a profile that cannot be read
// or a root that was named and is wrong warns.
func (rt *runtime) outlookStoreCheck(st *store.Store) check {
	const name = "outlook_store"
	if !rt.outlookOn {
		return check{Name: name, OK: true, Detail: "the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root); name --outlook-root DIR to turn it on"}
	}
	root := rt.outlookRoot
	if root == "" {
		var err error
		if root, err = outlookDefaultRoot(); err != nil {
			return check{Name: name, OK: true, Detail: "the new Outlook is not read on this machine: " + err.Error()}
		}
	}
	profiles, classic, skipped, err := outlookDiscover(root)
	var coded *errs.Coded
	switch {
	case rt.outlookDefault && errors.Is(err, outlookdesktop.ErrRootNotFound):
		return check{Name: name, OK: true, Detail: "the new Outlook for Mac is not installed (no profiles directory); nothing to read"}
	case errors.As(err, &coded):
		return check{Name: name, OK: true, Warn: true, Detail: coded.Message, Fix: coded.Fix}
	case rt.outlookDefault && err == nil && len(profiles) == 0 && len(skipped) == 0:
		return check{Name: name, OK: true, Detail: "the new Outlook for Mac has no profile; nothing to read"}
	case err != nil, len(profiles) == 0 && len(skipped) == 0:
		e := syncer.NoOutlookProfilesError(root, classic)
		return check{Name: name, OK: true, Warn: true, Detail: e.Message, Fix: e.Fix}
	}
	var state map[string]store.CalendarSource
	var details, fixes []string
	if st != nil {
		res, err := readCalendarSources(st, rt.ctx, store.CalendarSourcesFilter{Now: rt.now(), ReadInterval: syncer.OutlookMinReadInterval})
		if err != nil {
			details, fixes = append(details, "cannot read the Outlook state: "+err.Error()), append(fixes, "Run `m365crawl sync`.")
		}
		state = map[string]store.CalendarSource{}
		for _, r := range res.Rows {
			state[r.AccountID] = r
		}
	}
	for _, sp := range skipped {
		details, fixes = append(details, "profile "+sp.Name+" cannot be examined ("+sp.Reason+")"), append(fixes, "Give m365crawl access to the profile directory "+sp.Dir+" (Full Disk Access on macOS).")
	}
	for _, p := range profiles {
		row := state["outlook/"+p.Name]
		d, fix := outlookProfileState(p, row, rt.now())
		if row.Link == "config" || row.Link == "address" {
			d += "; linked to a Teams account (" + row.Link + ")"
		}
		details, fixes = append(details, d), append(fixes, fix)
	}
	c := check{Name: name, OK: true, Detail: strings.Join(details, "; ")}
	for _, f := range fixes {
		if f != "" {
			c.Warn, c.Fix = true, f
			break
		}
	}
	return c
}

// outlookProfileState is one profile's line of the check, and the fix when it is a problem.
func outlookProfileState(p outlookdesktop.Profile, row store.CalendarSource, now time.Time) (detail, fix string) {
	detail = "profile " + p.Name + ": "
	hdr, err := outlookOpenStore(p.StorePath)
	if err == nil {
		_ = hdr.Close()
	}
	var version hxstore.ErrStoreVersion
	switch {
	case errors.As(err, &version):
		return detail + fmt.Sprintf("the store is version %q and this m365crawl reads %q (unsupported_version)", rune(version.Found), rune(hxstore.KnownStoreVersions[0])), "Update m365crawl: this version cannot read the store."
	case err != nil:
		return detail + "the store cannot be read (unreadable): " + err.Error(), "Check that " + p.StorePath + " exists and is readable; on macOS give m365crawl Full Disk Access."
	}
	detail += "store version readable"
	if o := row.Outlook; o != nil {
		if f := o.Failure; f != nil {
			return detail + "; the last read failed (" + o.Status + "): " + f.Code + ": " + f.Message, firstOf(f.Fix, "Run `m365crawl sync` and read its error.")
		}
		if !o.LastReadAt.IsZero() {
			return detail + "; last read " + oneUnit(now.Sub(o.LastReadAt)) + " ago, " + o.Status, ""
		}
	}
	return detail + "; not read yet", ""
}
