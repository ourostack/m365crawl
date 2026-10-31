package cli

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// The values of implicit_sync for a read that did not sync because this machine's sources do not
// feed the archive it names with --db (#295). The archive is read as it is and nothing is written.
const (
	implicitSyncForeignArchive = "foreign_archive" // no sync of this machine wrote to it from this read's sources
	implicitSyncNoArchive      = "no_archive"      // there is no archive at that path, and a read does not create one
)

// syncOffNotFed is what turned the implicit sync off for a read of an archive this machine does not
// feed.
const syncOffNotFed = "this machine's sources do not feed this archive"

// isDefaultPath says whether path, absolute, is the default archive's; tests add their archives.
var isDefaultPath = func(path string) bool {
	def, err := defaultArchivePath()
	return err == nil && filepath.Clean(def) == filepath.Clean(path)
}

// userConfigDir is os.UserConfigDir; tests make it fail.
var userConfigDir = os.UserConfigDir

// linkFile is os.Link; tests make it fail.
var linkFile = os.Link

// machineIDLen is the length of a machine ID: 16 random bytes in hex.
const machineIDLen = 32

// machineID is this user's random machine ID, kept in m365crawl's user configuration directory
// (not beside an archive, so a copied archive never carries it). create makes one when there is
// none yet; "" means there is none and none could be made.
func machineID(create bool) string {
	base, err := userConfigDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "m365crawl")
	path := filepath.Join(dir, "machine-id")
	if id := readMachineID(path); id != "" || !create {
		return id
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return ""
	}
	// Written whole to a temp file, then linked into place: no reader sees a partial ID, and two
	// first syncs keep the one that was linked first.
	f, err := os.CreateTemp(dir, ".machine-id-*")
	if err != nil {
		return ""
	}
	b := make([]byte, machineIDLen/2)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	_, writeErr := f.WriteString(hex.EncodeToString(b))
	if errors.Join(writeErr, f.Close()) == nil {
		_ = linkFile(f.Name(), path)
	}
	_ = os.Remove(f.Name())
	return readMachineID(path)
}

func readMachineID(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // G304: m365crawl's own configuration file
	if _, hexErr := hex.DecodeString(string(b)); err != nil || hexErr != nil || len(b) != machineIDLen {
		return ""
	}
	return string(b)
}

// sourceKey is one source's identity in one archive: a hash of the machine ID, the source kind, its
// root ("" for the default root) and the archive's own path, so the archive holds no machine, path
// or account in clear, and a copy of it at another path, even on this machine, does not match.
func sourceKey(machine, kind, root, archive string) string {
	sum := sha256.Sum256([]byte("m365crawl source identity v1\x00" + machine + "\x00" + kind + "\x00" + root + "\x00" + archive))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// canonicalPath is the absolute path p with its symbolic links resolved as far as it exists, so
// one archive named through a link, from another working directory or before its file exists has
// one path.
func canonicalPath(p string) string {
	dir, rest := filepath.Clean(p), ""
	real, err := filepath.EvalSymlinks(dir)
	for err != nil && dir != filepath.Dir(dir) {
		dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest)
		real, err = filepath.EvalSymlinks(dir)
	}
	return filepath.Join(cmp.Or(real, dir), rest)
}

// sourceIdentity is the keys of the sources this run's sync reads: Teams first, then Outlook when it
// is on. It is nil without a machine ID; create makes one (a sync), a read never does.
func (rt *runtime) sourceIdentity(create bool) []string {
	id := machineID(create)
	if id == "" {
		return nil
	}
	archive := canonicalPath(rt.archiveRef())
	keys := []string{sourceKey(id, "teams", rt.rootRef(rt.root), archive)}
	if rt.outlookOn {
		keys = append(keys, sourceKey(id, "outlook", rt.rootRef(rt.outlookRoot), archive))
	}
	return keys
}

// rootRef is a named root as an absolute path, so the same folder named from two working
// directories is one source; "" (the default root) stays "".
func (rt *runtime) rootRef(root string) string {
	if root == "" {
		return ""
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	wd, err := getwd()
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Join(wd, root)
}

// isDefaultArchive says whether the read's archive is the default one, named or not.
func (rt *runtime) isDefaultArchive() bool {
	if rt.g.DB == "" || isDefaultPath(rt.archiveRef()) {
		return true
	}
	def, err := defaultArchivePath()
	if err != nil {
		return false
	}
	a, errA := os.Stat(def)
	b, errB := os.Stat(rt.dbPath)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// checkFeed decides, once per read and before anything could sync, whether the implicit sync and
// the registered-source refreshes may write into the archive. They may for the default archive and
// for one this machine's sources feed (fed). Otherwise the read reads the archive as it is, with
// implicit_sync no_archive or foreign_archive and a notice naming the explicit sync. --no-sync and
// a zero max age skip the check: nothing writes then anyway.
func (rt *runtime) checkFeed() error {
	if rt.feedChecked || rt.maxAge <= 0 {
		return nil
	}
	rt.feedChecked = true
	if rt.isDefaultArchive() {
		return nil
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		rt.notFed = implicitSyncNoArchive
	case err != nil:
		return asCoded(err)
	default:
		id, err := st.SourceIdentity(rt.ctx)
		_ = st.Close()
		if err == nil && rt.fed(id) {
			return nil
		}
		rt.notFed = implicitSyncForeignArchive
	}
	rt.maxAge, rt.syncOff = 0, syncOffNotFed
	return nil
}

// fed says whether this machine's sources feed the archive. An archive with a recorded identity is
// fed when it holds this machine's key, for this archive's path, of every source this read would
// sync; an Outlook source that is on only by default also counts when the archive never recorded an
// Outlook source, because it is this machine's own default (a sync with --outlook-root none fed it).
// An archive no sync recorded an identity in, written by an older m365crawl, is fed when its sync
// history names a Teams profile under this read's Teams root (legacyFed).
func (rt *runtime) fed(id store.SourceIdentity) bool {
	if len(id.Recorded) == 0 {
		return rt.legacyFed(id.TeamsSources)
	}
	want := rt.sourceIdentity(false)
	switch {
	case want == nil || !id.Recorded[want[0]]:
		return false
	case len(want) == 1 || id.Recorded[want[1]]:
		return true
	}
	for key := range id.Recorded {
		if strings.HasPrefix(key, "outlook:") {
			return false
		}
	}
	return rt.outlookDefault
}

// legacyFed is the evidence an archive from before source identities has: the Teams sources
// (<profile>|<origin>) its successful syncs read. It is fed when one of those Teams profiles is under
// this read's Teams root now: profiles come and go (a sign-out, Teams' move to another origin), and
// a fixture or another folder setup has other profile names. The first sync then records this
// machine's identity for the archive.
func (rt *runtime) legacyFed(sources []string) bool {
	root := rt.root
	if root == "" {
		root = teamsdesktop.DefaultRoot()
	}
	found, _, _ := discover(root)
	here := map[string]bool{}
	for _, src := range found {
		here[src.Profile] = true
	}
	for _, src := range sources {
		if profile, _, _ := strings.Cut(src, "|"); here[profile] {
			return true
		}
	}
	return false
}

// noteNotFed marks a read that did not sync because this machine does not feed its archive, with a
// notice naming the explicit sync that would. Each read's result passes here once: from read, or
// from noteRegisteredSyncOff for a registered-source read.
func (rt *runtime) noteNotFed(res result) {
	if rt.notFed == "" {
		return
	}
	res.setImplicitSync(rt.notFed)
	run := rt.recoveryCommand(true, "", "sync")
	if strings.HasPrefix(run, "no executable") {
		run = rt.archiveCommand("sync")
	}
	path := rt.archiveRef()
	if rt.notFed == implicitSyncNoArchive {
		res.addNotice("There is no archive at " + path + ", and a read does not create one, so nothing was read or written. To create it from this machine's Teams and Outlook data, run `" + run + "`.")
		return // the text-mode hint is the read's own "run <sync>" line
	}
	res.addNotice("This read did not sync: no sync on this machine has written to the archive at " + path +
		" from the Teams and Outlook folders this read uses (it may be a copy from another machine or another folder, or from an older m365crawl that cannot be matched to this machine), so it was read as it is and nothing was written to it. " +
		"To add this machine's data to it on purpose, run `" + run + "` once; later reads then keep it fresh.")
	if rt.format == output.Text {
		rt.hint("this read did not sync: this machine's sources do not feed this archive; run `" + run + "` to sync into it")
	}
}
