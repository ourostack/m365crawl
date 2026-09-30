package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/render"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const staleSyncAfter = 24 * time.Hour

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
	sources, other, derr := teamsdesktop.Discover(root)
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
	switch code {
	case errs.CodeTeamsNotInstalled:
		cs = append(cs, check{Name: "full_disk_access", Detail: "not checked: Teams is not installed", Fix: "Fix teams_installed first."})
	case errs.CodeNoFullDiskAccess:
		cs = append(cs, check{Name: "full_disk_access", Detail: coded.Message, Fix: fdaFix()})
	default:
		cs = append(cs, check{Name: "full_disk_access", OK: true, Detail: "the Teams container is readable"})
	}
	// teams_origin
	switch {
	case code == errs.CodeNoTeamsOrigin:
		cs = append(cs, check{Name: "teams_origin", Detail: coded.Message, Fix: coded.Fix})
	case derr != nil:
		cs = append(cs, check{Name: "teams_origin", Detail: "not checked: the Teams data is not readable", Fix: "Fix the checks above first."})
	default:
		d := fmt.Sprintf("%d Teams origin(s) found", len(sources))
		if len(other) > 0 {
			d += "; ignoring non-Teams origins: " + strings.Join(other, ", ")
		}
		cs = append(cs, check{Name: "teams_origin", OK: true, Detail: d})
	}
	return append(cs, rt.archiveChecks()...)
}

func (rt *runtime) archiveChecks() []check {
	var cs []check
	cs = append(cs, rt.writableCheck())
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		const d = "no archive yet; the first sync creates it"
		return append(cs,
			check{Name: "schema_version", OK: true, Detail: d},
			check{Name: "fts", OK: true, Detail: d},
			check{Name: "last_sync_age", OK: true, Warn: true, Detail: "never synced", Fix: "Run `teamscrawl sync`."})
	case err != nil:
		fix := "Check that " + rt.dbPath + " is a teamscrawl archive; move it aside and run `teamscrawl sync` to rebuild it."
		return append(cs,
			check{Name: "schema_version", Detail: "cannot open the archive: " + err.Error(), Fix: fix},
			check{Name: "fts", Detail: "cannot open the archive", Fix: fix},
			check{Name: "last_sync_age", Detail: "cannot open the archive", Fix: fix})
	}
	defer func() { _ = st.Close() }()
	row, err := st.Status(rt.ctx)
	if err != nil {
		fix := "Run `teamscrawl sync`; if it fails, move " + rt.dbPath + " aside and sync again."
		return append(cs, check{Name: "schema_version", Detail: "cannot read the archive: " + err.Error(), Fix: fix},
			check{Name: "fts", Detail: "cannot read the archive", Fix: fix}, check{Name: "last_sync_age", Detail: "cannot read the archive", Fix: fix})
	}
	switch {
	case row.SchemaVersion == store.SchemaVersion:
		cs = append(cs, check{Name: "schema_version", OK: true, Detail: fmt.Sprintf("schema v%d", row.SchemaVersion)})
	case row.SchemaVersion > store.SchemaVersion:
		cs = append(cs, check{Name: "schema_version", Detail: fmt.Sprintf("archive is schema v%d, this teamscrawl knows v%d", row.SchemaVersion, store.SchemaVersion), Fix: "Update teamscrawl."})
	default:
		cs = append(cs, check{Name: "schema_version", Detail: fmt.Sprintf("archive is schema v%d, expected v%d", row.SchemaVersion, store.SchemaVersion), Fix: "Run `teamscrawl sync` to migrate the archive."})
	}
	if row.FTSPresent {
		cs = append(cs, check{Name: "fts", OK: true, Detail: "full-text indexes present"})
	} else {
		cs = append(cs, check{Name: "fts", Detail: "full-text indexes are missing", Fix: "Run `teamscrawl sync`; if they stay missing, move " + rt.dbPath + " aside and sync again."})
	}
	switch {
	case row.LastSuccessAt.IsZero():
		cs = append(cs, check{Name: "last_sync_age", OK: true, Warn: true, Detail: "no successful sync yet", Fix: "Run `teamscrawl sync`."})
	case rt.now().Sub(row.LastSuccessAt) > staleSyncAfter:
		cs = append(cs, check{Name: "last_sync_age", OK: true, Warn: true, Detail: "last successful sync " + rt.now().Sub(row.LastSuccessAt).Round(time.Minute).String() + " ago", Fix: "Run `teamscrawl sync`."})
	default:
		cs = append(cs, check{Name: "last_sync_age", OK: true, Detail: "last successful sync " + rt.now().Sub(row.LastSuccessAt).Round(time.Second).String() + " ago"})
	}
	return cs
}

// writableCheck proves the archive can be written without touching it: it creates and removes a
// scratch file beside it (in the nearest existing directory when the archive dir is missing).
func (rt *runtime) writableCheck() check {
	dir := filepath.Dir(rt.dbPath)
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	fix := "Make " + dir + " writable, or pass --db with a path you can write."
	f, err := os.CreateTemp(dir, ".teamscrawl-doctor-*")
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
