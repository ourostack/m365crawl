package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
)

func doctorCheck(t *testing.T, e *env, name string) (check map[string]any, code int) {
	t.Helper()
	code, out, _ := e.run("doctor", "--json")
	for _, c := range decode(t, out)["checks"].([]any) {
		if m := c.(map[string]any); m["name"] == name {
			return m, code
		}
	}
	t.Fatalf("no %s check in %s", name, out)
	return nil, 0
}

func TestDoctorTranscriptsBrowserEdge(t *testing.T) {
	e := newEnv(t)
	var asked []string
	old := findBrowser
	findBrowser = func(pref string) (string, browser.Kind, error) {
		asked = append(asked, pref)
		return old(pref)
	}
	t.Cleanup(func() { findBrowser = old })
	t.Setenv("M365CRAWL_BROWSER", "chrome")
	c, _ := doctorCheck(t, e, "transcripts_browser")
	if c["ok"] != true || c["warn"] != nil || c["detail"] != "edge at "+testBrowserPath+" (transcripts fetch runs it headless; doctor does not start it)" {
		t.Fatalf("check %v", c)
	}
	if strings.Join(asked, ",") != "chrome" {
		t.Fatalf("doctor asks for the browser M365CRAWL_BROWSER names: %v", asked)
	}
}

func TestDoctorTranscriptsBrowserNone(t *testing.T) {
	e := newEnv(t)
	e.sync()
	old := findBrowser
	t.Cleanup(func() { findBrowser = old })
	for _, err := range []error{errs.NoBrowser(), errors.New("plain")} {
		findBrowser = func(string) (string, browser.Kind, error) { return "", "", err }
		c, code := doctorCheck(t, e, "transcripts_browser")
		if c["ok"] != true || c["warn"] != true || c["detail"] != "none: transcripts fetch needs Edge or Chrome" || c["fix"] != errs.NoBrowser().Fix {
			t.Fatalf("check %v", c)
		}
		if code != 0 {
			t.Fatalf("no browser is a warning, never a failure: exit %d", code)
		}
	}
}

func TestDoctorTranscriptsProfileMode(t *testing.T) {
	e := newEnv(t)
	dir := browser.ProfileDir(e.db)
	c, _ := doctorCheck(t, e, "transcripts_profile_mode")
	if c["ok"] != true || c["warn"] != nil || !strings.HasPrefix(c["detail"].(string), "no browser profile yet") {
		t.Fatalf("no profile: %v", c)
	}
	e.emptyArchive() // the archive's directory, private on Windows, which the profile inherits
	if err := browser.EnsureProfileDir(dir); err != nil {
		t.Fatal(err)
	}
	c, _ = doctorCheck(t, e, "transcripts_profile_mode")
	if c["ok"] != true || c["warn"] != nil || c["detail"] != "the browser profile "+dir+" is readable by its owner only" {
		t.Fatalf("private profile: %v", c)
	}
	oldPrivate, oldStat := privateDir, statProfile
	t.Cleanup(func() { privateDir, statProfile = oldPrivate, oldStat })
	privateDir = func(string) (bool, error) { return false, nil }
	c, code := doctorCheck(t, e, "transcripts_profile_mode")
	if c["warn"] != true || !strings.Contains(c["detail"].(string), "can be read by other users") || !strings.Contains(c["fix"].(string), dir) || code != 0 {
		t.Fatalf("open profile: %v, exit %d", c, code)
	}
	privateDir = func(string) (bool, error) { return false, errors.New("no access list") }
	if c, _ = doctorCheck(t, e, "transcripts_profile_mode"); c["warn"] != true || !strings.Contains(c["detail"].(string), "no access list") {
		t.Fatalf("unreadable permissions: %v", c)
	}
	privateDir = oldPrivate
	statProfile = func(string) (os.FileInfo, error) {
		return nil, &fs.PathError{Op: "stat", Path: dir, Err: errors.New("denied")}
	}
	if c, _ = doctorCheck(t, e, "transcripts_profile_mode"); c["warn"] != true || !strings.Contains(c["detail"].(string), "cannot examine") {
		t.Fatalf("stat failure: %v", c)
	}
	statProfile = oldStat
	// A file where the profile belongs.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ = doctorCheck(t, e, "transcripts_profile_mode"); c["warn"] != true || c["detail"] != dir+" is not a directory" {
		t.Fatalf("a file: %v", c)
	}
}

// Doctor reads the profile's permissions and nothing in it.
func TestDoctorNeverOpensTheProfile(t *testing.T) {
	if os.Geteuid() == 0 || goruntime.GOOS == "windows" {
		t.Skip("root reads a chmod 000 directory, and Windows has no such mode")
	}
	e := newEnv(t)
	dir := browser.ProfileDir(e.db)
	if err := browser.EnsureProfileDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cookies"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restoring the test directory
	c, _ := doctorCheck(t, e, "transcripts_profile_mode")
	if c["ok"] != true || c["warn"] != nil {
		t.Fatalf("a profile doctor cannot enter is still examined by its mode alone: %v", c)
	}
}

// M365CRAWL_BROWSER naming nothing on this machine is said as such, with the variable named.
func TestDoctorTranscriptsBrowserEnvNamesNothing(t *testing.T) {
	e := newEnv(t)
	old := findBrowser
	t.Cleanup(func() { findBrowser = old })
	findBrowser = func(string) (string, browser.Kind, error) { return "", "", errs.NoBrowser() }
	t.Setenv("M365CRAWL_BROWSER", "/nowhere/msedge")
	c, code := doctorCheck(t, e, "transcripts_browser")
	if c["warn"] != true || c["detail"] != `M365CRAWL_BROWSER is "/nowhere/msedge", which names no browser on this machine` ||
		!strings.Contains(c["fix"].(string), "M365CRAWL_BROWSER") || code != 0 {
		t.Fatalf("check %v, exit %d", c, code)
	}
}

func TestProfileFixFitsThePlatform(t *testing.T) {
	if f := profileFix("/a b/browser", "darwin"); !strings.HasPrefix(f, "Run chmod 700 '/a b/browser', or move it aside") {
		t.Errorf("darwin: %s", f)
	}
	if f := profileFix(`C:\x\browser`, "windows"); strings.Contains(f, "chmod") || !strings.Contains(f, "SYSTEM") || !strings.Contains(f, `C:\x\browser`) {
		t.Errorf("windows: %s", f)
	}
}
