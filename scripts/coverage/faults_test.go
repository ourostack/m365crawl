package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSDKMetadataNumbersRespectPlatformIntBounds(t *testing.T) {
	oldOutput, oldParse := commandOutput, parseSource
	t.Cleanup(func() { commandOutput, parseSource = oldOutput, oldParse })
	commandOutput = func(*exec.Cmd) ([]byte, error) { return nil, nil }
	for _, field := range []string{"Pos", "NumStmt"} {
		for _, literal := range []string{"2147483647", "2147483648", "4294967295", "0xffffffff", "037777777777", "0b11111111111111111111111111111111", "4294967296", "-1"} {
			t.Run(field+"/"+literal, func(t *testing.T) {
				pos, weight := "1, 2, 65537", "1"
				if field == "Pos" {
					pos = literal + ", 2, 65537"
				} else {
					weight = literal
				}

				text := "package p\nvar CoverageProof = struct{Pos []uint32; NumStmt []uint32}{Pos: []uint32{" + pos + "}, NumStmt: []uint32{" + weight + "}}"
				parseSource = func(fset *token.FileSet, name string, src any, mode parser.Mode) (*ast.File, error) {
					return parser.ParseFile(fset, name, text, mode)
				}
				n, parseErr := strconv.ParseUint(literal, 0, 32)
				wantRefusal := parseErr != nil || strconv.IntSize == 32 && n > 2147483647
				blocks, err := sourceBlocks("synthetic.go")
				if wantRefusal {
					if err == nil || len(blocks) != 0 || !strings.HasPrefix(err.Error(), "invalid SDK block number") {
						t.Fatalf("out-of-range SDK number did not refuse: blocks=%d err=%v", len(blocks), err)
					}
					return
				}
				if err != nil || len(blocks) != 1 {
					t.Fatalf("lawful SDK number refused: blocks=%d err=%v", len(blocks), err)
				}
				got := blocks[0].startLine
				if field == "NumStmt" {
					got = blocks[0].statements
				}
				if strconv.FormatInt(int64(got), 10) != strconv.FormatUint(n, 10) {
					t.Fatalf("SDK number changed during conversion: got=%d want=%d", got, n)
				}
			})
		}
	}
}

func TestSDKMetadataNativeIntParserRefusalIsNotPartialInventory(t *testing.T) {
	oldOutput, oldParse, oldInt := commandOutput, parseSource, sdkNumberInt
	t.Cleanup(func() { commandOutput, parseSource, sdkNumberInt = oldOutput, oldParse, oldInt })
	commandOutput = func(*exec.Cmd) ([]byte, error) { return nil, nil }
	text := "package p\nvar CoverageProof = struct{Pos []uint32; NumStmt []uint32}{Pos: []uint32{1,2,65537}, NumStmt: []uint32{0x80000000}}"
	parseSource = func(fset *token.FileSet, name string, src any, mode parser.Mode) (*ast.File, error) {
		return parser.ParseFile(fset, name, text, mode)
	}
	sdkNumberInt = func(s string) (int, error) {
		if _, err := strconv.ParseInt(s, 10, 32); err != nil {
			return 0, err
		}
		return strconv.Atoi(s)
	}
	if blocks, err := sourceBlocks("synthetic.go"); err == nil || len(blocks) != 0 || err.Error() != "invalid SDK block number: exceeds platform int" {
		t.Fatal("native int parsing refusal was ignored")
	}
}

func TestSDKMetadataRefusesMissingOrInvalidInventory(t *testing.T) {
	oldOutput, oldParse := commandOutput, parseSource
	t.Cleanup(func() { commandOutput, parseSource = oldOutput, oldParse })
	commandOutput = func(*exec.Cmd) ([]byte, error) { return nil, nil }
	for _, text := range []string{
		"package p\nvar unrelated int",
		"package p\nvar CoverageProof = 1",
		"package p\nvar CoverageProof = struct{}{}",
		"package p\nvar CoverageProof = struct{Pos []int; NumStmt []int}{Pos: []int{1}, NumStmt: []int{1}}",
		"package p\nvar CoverageProof = struct{Pos []int; NumStmt []int}{Pos: []int{bad}, NumStmt: []int{1}}",
		"package p\nvar CoverageProof = struct{Pos []int; NumStmt []int}{[]int{1}, []int{1}}",
		"package p\nvar CoverageProof = struct{Pos []int; NumStmt []int}{Pos: 1, NumStmt: []int{1}}",
		"package p\nvar CoverageProof = struct{Pos []int; NumStmt []int}{Pos: []int{4294967296}, NumStmt: []int{1}}",
	} {
		parseSource = func(fset *token.FileSet, name string, src any, mode parser.Mode) (*ast.File, error) {
			return parser.ParseFile(fset, name, text, mode)
		}
		if _, err := sourceBlocks("synthetic.go"); err == nil {
			t.Fatalf("invalid SDK inventory accepted: %s", text)
		}
	}
}

func TestCoverageToolAndFileFailuresAreNotSuccess(t *testing.T) {
	root, profile := fixtureProfile(t, true)
	oldOutput, oldParse, oldClose := commandOutput, parseSource, closeProfile
	t.Cleanup(func() { commandOutput, parseSource, closeProfile = oldOutput, oldParse, oldClose })
	commandOutput = func(*exec.Cmd) ([]byte, error) { return nil, errors.New("synthetic tool failed") }
	report, err := checkScope(root, "synthetic.test/coverage", profile, map[string][]string{"sample.go": nil})
	if err == nil || !strings.Contains(report, "block inventory failed") {
		t.Fatalf("tool failure accepted: %v %s", err, report)
	}
	commandOutput = oldOutput
	parseSource = func(fset *token.FileSet, name string, src any, mode parser.Mode) (*ast.File, error) {
		if filepath.Base(name) == "instrumented.go" {
			return nil, errors.New("synthetic parser failure")
		}
		return oldParse(fset, name, src, mode)
	}
	if _, err := sourceBlocks(filepath.Join(root, "sample.go")); err == nil {
		t.Fatal("instrumentation parser failure accepted")
	}
	parseSource = oldParse
	closeProfile = func(f *os.File) error {
		_ = f.Close()
		return errors.New("synthetic close failure")
	}
	report, err = checkScope(root, "synthetic.test/coverage", profile, map[string][]string{"sample.go": nil})
	if err == nil || !strings.Contains(report, "close failure") {
		t.Fatalf("profile close failure accepted: %v %s", err, report)
	}
}

func TestNativeScopeAndEntrypointFailures(t *testing.T) {
	oldOutput, oldAbs, oldRel := commandOutput, absolutePath, relativePath
	t.Cleanup(func() { commandOutput, absolutePath, relativePath = oldOutput, oldAbs, oldRel })
	commandOutput = func(*exec.Cmd) ([]byte, error) { return []byte("invalid JSON"), nil }
	if _, err := nativeScope(t.TempDir()); err == nil {
		t.Fatal("invalid native package inventory accepted")
	}
	commandOutput = func(*exec.Cmd) ([]byte, error) {
		return []byte(`{"Dir":"synthetic","ImportPath":"github.com/ourostack/m365crawl/internal/outlookowa","GoFiles":["source_windows.go"]}`), nil
	}
	relativePath = func(string, string) (string, error) { return "", errors.New("synthetic relative path failure") }
	if _, err := nativeScope(t.TempDir()); err == nil {
		t.Fatal("invalid relative source path accepted")
	}
	relativePath = oldRel
	commandOutput = func(*exec.Cmd) ([]byte, error) { return nil, nil }
	if _, err := nativeScope(t.TempDir()); err == nil {
		t.Fatal("missing native Windows providers accepted")
	}
	var out, stderr bytes.Buffer
	absolutePath = func(string) (string, error) { return "", errors.New("synthetic absolute path failure") }
	if code := run(nil, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "absolute path failure") {
		t.Fatalf("absolute path failure exit = %d: %s", code, stderr.String())
	}
}

func TestCoverageMainPreservesUsageExit(t *testing.T) {
	oldArgs, oldExit := os.Args, exitProcess
	t.Cleanup(func() { os.Args, exitProcess = oldArgs, oldExit })
	os.Args = []string{"coverage", "-invalid"}
	code := -1
	exitProcess = func(n int) { code = n }
	main()
	if code != 2 {
		t.Fatalf("main usage exit = %d", code)
	}
}
