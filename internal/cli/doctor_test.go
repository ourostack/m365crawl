package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func checks(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, c := range m["checks"].([]any) {
		cm := c.(map[string]any)
		out[cm["name"].(string)] = cm
	}
	return out
}

var doctorNames = []string{"teams_installed", "full_disk_access", "teams_origin", "database_writable", "schema_version", "fts", "last_sync_age"}

func TestDoctorAllPass(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("doctor")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	m := decode(t, stdout)
	cs := checks(t, m)
	for _, n := range doctorNames {
		c, ok := cs[n]
		if !ok {
			t.Fatalf("missing check %q", n)
		}
		if c["ok"] != true {
			t.Errorf("%s failed: %v", n, c)
		}
		for _, k := range []string{"name", "ok", "detail", "fix"} {
			if _, has := c[k]; !has {
				t.Errorf("%s lacks %q", n, k)
			}
		}
	}
	if m["ok"] != true {
		t.Fatalf("ok = %v", m["ok"])
	}
}

func TestDoctorBeforeFirstSyncPassesWithWarning(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("doctor")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	c := checks(t, decode(t, stdout))["last_sync_age"]
	if c["ok"] != true || c["warn"] != true || c["fix"] == "" {
		t.Fatalf("never-synced must warn, not fail: %v", c)
	}
	if _, err := os.Stat(e.db); err == nil {
		t.Fatal("doctor must not create the archive")
	}
}

func TestDoctorFailExit3(t *testing.T) {
	e := newEnv(t)
	e.root = filepath.Join(t.TempDir(), "absent")
	code, stdout, stderr := e.run("doctor", "--json")
	if code != 3 {
		t.Fatalf("exit %d", code)
	}
	m := decode(t, stdout)
	if m["ok"] != false {
		t.Fatalf("ok = %v", m["ok"])
	}
	c := checks(t, m)["teams_installed"]
	if c["ok"] != false || c["fix"] == "" {
		t.Fatalf("teams_installed = %v", c)
	}
	if er := errorOf(t, stderr); er["code"] != "doctor_failed" {
		t.Fatalf("error = %v", er)
	}
}

func TestDoctorNoFDA(t *testing.T) {
	skipIfRoot(t)
	e := newEnv(t)
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: restoring a directory so TempDir cleanup works
	e.root = locked
	code, stdout, _ := e.run("doctor")
	c := checks(t, decode(t, stdout))["full_disk_access"]
	if code != 3 || c["ok"] != false || c["fix"] == "" {
		t.Fatalf("exit %d, %v", code, c)
	}
}
