package coveragegate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gateFixture(t *testing.T, coverAll bool) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix coverage gate; Windows uses check-coverage.ps1")
	}
	root := t.TempDir()
	for _, dir := range []string{"scripts", "internal/fixture", "internal/other"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("../check-coverage.sh")
	if err != nil {
		t.Fatal(err)
	}
	test := "package fixture\nimport \"testing\"\nfunc TestRead(t *testing.T) { if Read(true) != 1 { t.Fatal(\"true\") }"
	if coverAll {
		test += "; if Read(false) != 0 { t.Fatal(\"false\") }"
	}
	test += " }\n"
	files := map[string]string{
		"go.mod":                        "module example.invalid/coveragefixture\n\ngo 1.27\n",
		"scripts/check-coverage.sh":     string(script),
		"internal/fixture/read.go":      "package fixture\nfunc Read(ok bool) int {\n if ok {\n  return 1\n }\n return 0\n}\n",
		"internal/fixture/read_test.go": test,
		"internal/other/read.go":        "package other\nfunc Other() int { return 7 }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile := filepath.Join(root, "coverage.out")
	command := exec.Command("go", "test", "-count=1", "-coverprofile=coverage.out", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic profile: %v\n%s", err, output)
	}
	return root, profile
}

func runGate(t *testing.T, root, profile string) (string, error) {
	t.Helper()
	command := exec.Command("bash", "scripts/check-coverage.sh")
	command.Dir = root
	command.Env = append(os.Environ(), "COVERAGE_PACKAGES=./internal/fixture", "COVERAGE_PROFILE="+profile)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestGateReportsExactUncoveredBlockWithoutChangingFailure(t *testing.T) {
	root, profile := gateFixture(t, false)
	output, err := runGate(t, root, profile)
	if err == nil {
		t.Fatal("missing statement passed the gate")
	}
	if !strings.Contains(output, "internal/fixture/read.go:6.2,6.10 1 0") {
		t.Fatalf("missing exact uncovered source block:\n%s", output)
	}
	if strings.Contains(output, "internal/other") || strings.Contains(output, "coverage OK") {
		t.Fatalf("unselected package or success leaked into failed gate:\n%s", output)
	}
}

func TestGateCoveredSourceKeepsSuccessWithoutBlockDiagnostics(t *testing.T) {
	root, profile := gateFixture(t, true)
	output, err := runGate(t, root, profile)
	if err != nil || !strings.Contains(output, "coverage OK") {
		t.Fatalf("covered fixture did not pass: %v\n%s", err, output)
	}
	if strings.Contains(output, "uncovered statement blocks") || strings.Contains(output, "internal/other") {
		t.Fatalf("green gate emitted unselected failure diagnostics:\n%s", output)
	}
}
