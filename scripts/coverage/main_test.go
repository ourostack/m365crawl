package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureProfile(t *testing.T, all bool, module ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mod := "synthetic.test/coverage"
	if len(module) > 0 {
		mod = module[0]
	}
	write("go.mod", "module "+mod+"\n\ngo 1.25\n")
	write("sample.go", "package sample\nfunc Called(x bool) int { if x { return 1 }; return 2 }\nfunc Uncalled() int { return 3 }\n")
	body := "package sample\nimport \"testing\"\nfunc TestCalled(t *testing.T) { if Called(true)!=1 { t.Fatal(\"wrong\") } }\n"
	if all {
		body += "func TestRemaining(t *testing.T) { if Called(false)!=2 || Uncalled()!=3 { t.Fatal(\"wrong\") } }\n"
	}
	write("sample_test.go", body)
	profile := filepath.Join(root, "actual.cover")
	cmd := exec.Command(goExe(), "test", "-count=1", "-coverprofile="+profile, ".") //nolint:gosec // G204: exact SDK and fixed arguments, synthetic test module only.
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture coverage: %v\n%s", err, out)
	}
	return root, profile
}

func TestStrictCoverageRejectsRealUncoveredStatements(t *testing.T) {
	root, profile := fixtureProfile(t, false)
	report, err := checkScope(root, "synthetic.test/coverage", profile, map[string][]string{"sample.go": nil})
	if err == nil || !strings.Contains(report, "Uncalled") || !strings.Contains(report, "Called") || !strings.Contains(report, "zero-hit") {
		t.Fatalf("uncovered functions accepted: %v\n%s", err, report)
	}
}

func TestStrictCoverageAcceptsOnlyCompleteActualProfile(t *testing.T) {
	root, profile := fixtureProfile(t, true)
	report, err := checkScope(root, "synthetic.test/coverage", profile, map[string][]string{"sample.go": nil})
	if err != nil || !strings.Contains(report, "100.0%") {
		t.Fatalf("complete actual profile refused: %v\n%s", err, report)
	}
}

func TestStrictCoverageRejectsMissingProfileFileAndFunction(t *testing.T) {
	root, profile := fixtureProfile(t, true)
	for _, tc := range []struct {
		name    string
		profile string
		scope   map[string][]string
		want    string
	}{
		{"profile", filepath.Join(root, "absent.cover"), map[string][]string{"sample.go": nil}, "profile"},
		{"file", profile, map[string][]string{"absent.go": nil}, "absent.go"},
		{"function", profile, map[string][]string{"sample.go": {"Missing"}}, "Missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := checkScope(root, "synthetic.test/coverage", tc.profile, tc.scope)
			if err == nil || !strings.Contains(report, tc.want) {
				t.Fatalf("missing input accepted: %v\n%s", err, report)
			}
		})
	}
	f, err := os.OpenFile(filepath.Join(root, "sample.go"), os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: generated synthetic test module under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("func NewlyRequired() int { return 4 }\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	report, err := checkScope(root, "synthetic.test/coverage", profile, map[string][]string{"sample.go": nil})
	if err == nil || !strings.Contains(report, "NewlyRequired") || !strings.Contains(report, "missing block") {
		t.Fatalf("missing function profile accepted: %v\n%s", err, report)
	}
}

func TestStrictProfileParserRejectsMalformedInputs(t *testing.T) {
	for _, body := range []string{"", "mode: imaginary\n", "mode: count\nbad\n", "mode: count\nsynthetic.test/coverage/sample.go:1.1,1.2 1 -1\n"} {
		if _, err := parseProfile(strings.NewReader(body)); err == nil {
			t.Fatalf("malformed profile accepted: %q", body)
		}
	}

}

func TestStrictCoverageRejectsInapplicableEmptyScope(t *testing.T) {
	root, profile := fixtureProfile(t, true)
	report, err := checkScope(root, "synthetic.test/coverage", profile, map[string][]string{})
	if err == nil || !strings.Contains(report, "empty scope") {
		t.Fatalf("empty scope accepted: %v\n%s", err, report)
	}
}
