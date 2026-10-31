//go:build e2e

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A read never fills an archive this machine's sources do not feed: a missing --db stays missing,
// and an archive another machine synced is read as it is until an explicit sync adopts it (#295).
func TestE2EReadsDoNotFillAForeignArchive(t *testing.T) {
	e := newEnv(t)
	scratch := filepath.Join(e.tmp, "scratch", "m365crawl.db")
	m := ok(t, e.run("messages", "--teams-root", e.root, "--db", scratch))
	if m["implicit_sync"] != "no_archive" || m["needs_sync"] != true {
		t.Fatalf("a read on a missing archive = %v", m)
	}
	if _, err := os.Stat(filepath.Dir(scratch)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the read created the archive or its folder: %v", err)
	}

	other := newEnv(t)                                           // another machine, with its own home and machine ID
	shared := filepath.Join(other.tmp, "shared", "m365crawl.db") // a new folder: Windows refuses an existing unprotected one
	mustExit(t, other.run("sync", "--teams-root", other.root, "--db", shared), 0)
	read := []string{"messages", "--teams-root", other.root, "--db", shared, "--max-age", "1ns"}
	m = ok(t, e.run(read...))
	if m["implicit_sync"] != "foreign_archive" || m["synced"] != nil || m["count"] == float64(0) {
		t.Fatalf("an archive another machine synced = %v", m)
	}
	ran := func(env *env) map[string]any {
		res := env.run(read...)
		mustExit(t, res, 0)
		return mustJSON(t, res.stdout)
	}
	if m = ran(other); m["implicit_sync"] != "ran" {
		t.Fatalf("the machine that synced it = %v", m)
	}
	mustExit(t, e.run("sync", "--teams-root", other.root, "--db", shared), 0)
	if m = ran(e); m["implicit_sync"] != "ran" {
		t.Fatalf("after this machine's explicit sync = %v", m)
	}
}
