// coverage verifies SDK-generated block metadata against an actual native test profile.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type block struct {
	startLine, startCol, endLine, endCol int
	statements                           int
}

type function struct {
	name       string
	start, end token.Position
}

// The gate's failure tests replace only OS/tool boundaries; real profiles and AST matching stay real.
var (
	commandOutput = func(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }
	parseSource   = parser.ParseFile
	absolutePath  = filepath.Abs
	relativePath  = filepath.Rel
	exitProcess   = os.Exit
	closeProfile  = (*os.File).Close
	coverageScope = nativeScope
	sdkNumberInt  = strconv.Atoi
)

func goExe() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // SA1019: this go-run-only gate must use the exact SDK that built it, not a different PATH SDK.
}

func parseProfile(r io.Reader) (map[string]map[block]uint64, error) {
	scan := bufio.NewScanner(r)
	if !scan.Scan() || scan.Text() != "mode: set" && scan.Text() != "mode: count" && scan.Text() != "mode: atomic" {
		return nil, errors.New("missing or invalid coverage mode")
	}
	out := map[string]map[block]uint64{}
	for scan.Scan() {
		line := scan.Text()
		i := strings.LastIndex(line, ":")
		if i < 1 {
			return nil, fmt.Errorf("invalid coverage record %q", line)
		}
		var b block
		var count uint64
		var extra string
		n, err := fmt.Sscanf(line[i+1:], "%d.%d,%d.%d %d %d %s", &b.startLine, &b.startCol, &b.endLine, &b.endCol, &b.statements, &count, &extra)
		if n != 6 || !errors.Is(err, io.EOF) || b.startLine < 1 || b.startCol < 1 || b.endLine < b.startLine || b.endCol < 1 || b.statements < 0 {
			return nil, fmt.Errorf("invalid coverage record %q", line)
		}
		path := line[:i]
		if out[path] == nil {
			out[path] = map[block]uint64{}
		}
		out[path][b] += count
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("profile has no blocks")
	}
	return out, nil
}

func sourceFunctions(path string) ([]function, error) {
	fset := token.NewFileSet()
	f, err := parseSource(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []function
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			name := fn.Name.Name
			if fn.Recv != nil {
				receiver := fn.Recv.List[0].Type
				if star, ok := receiver.(*ast.StarExpr); ok {
					receiver = star.X
				}
				if id, ok := receiver.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			out = append(out, function{name, fset.Position(fn.Body.Pos()), fset.Position(fn.Body.End())})
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if v, ok := n.(*ast.ValueSpec); ok {
				allDirect := true
				for _, value := range v.Values {
					if _, ok := value.(*ast.FuncLit); !ok {
						allDirect = false
					}
				}
				if allDirect && len(v.Values) > 0 {
					for i, value := range v.Values {
						fn := value.(*ast.FuncLit)
						out = append(out, function{"initializer:" + v.Names[i].Name, fset.Position(fn.Body.Pos()), fset.Position(fn.Body.End())})
					}
					return false
				}
			}
			fn, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			p := fset.Position(fn.Pos())
			out = append(out, function{fmt.Sprintf("literal@%d:%d", p.Line, p.Column), fset.Position(fn.Body.Pos()), fset.Position(fn.Body.End())})
			return false
		})
	}
	return out, nil
}

// Ask the same SDK that produced the profile for the complete executable block inventory; exact spans and statement weights reject partial/stale profiles.
func sourceBlocks(path string) ([]block, error) {
	dir, err := os.MkdirTemp("", "m365crawl-coverage-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	instrumented := filepath.Join(dir, "instrumented.go")
	cmd := exec.Command(goExe(), "tool", "cover", "-mode=count", "-var=CoverageProof", "-o="+instrumented, path) //nolint:gosec // G204: fixed SDK executable and argv, source path is an argument, never shell input.
	if output, err := commandOutput(cmd); err != nil {
		return nil, fmt.Errorf("instrument source: %w: %s", err, output)
	}
	f, err := parseSource(token.NewFileSet(), instrumented, nil, 0)
	if err != nil {
		return nil, err
	}
	var positions, weights []int
	var metadataErr error
	found, hasPositions, hasWeights := false, false, false
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok || len(v.Names) != 1 || v.Names[0].Name != "CoverageProof" || len(v.Values) != 1 {
			return true
		}
		found = true
		init, ok := v.Values[0].(*ast.CompositeLit)
		if !ok {
			metadataErr = errors.New("invalid SDK block initializer")
			return false
		}
		for _, elem := range init.Elts {
			kv, ok := elem.(*ast.KeyValueExpr)
			if !ok {
				metadataErr = errors.New("invalid SDK block field")
				return false
			}
			key, keyOK := kv.Key.(*ast.Ident)
			values, valuesOK := kv.Value.(*ast.CompositeLit)
			if !keyOK || !valuesOK {
				metadataErr = errors.New("invalid SDK block array")
				return false
			}
			var nums []int
			for _, value := range values.Elts {
				lit, ok := value.(*ast.BasicLit)
				if !ok {
					metadataErr = errors.New("invalid SDK block number")
					return false
				}
				n, err := strconv.ParseUint(lit.Value, 0, 32)
				if err != nil {
					metadataErr = fmt.Errorf("invalid SDK block number: %w", err)
					return false
				}
				number, err := sdkNumberInt(strconv.FormatUint(n, 10))
				if err != nil {
					metadataErr = errors.New("invalid SDK block number: exceeds platform int")
					return false
				}
				nums = append(nums, number)
			}
			switch key.Name {
			case "Pos":
				positions = nums
				hasPositions = true
			case "NumStmt":
				weights = nums
				hasWeights = true
			}
		}
		return false
	})
	if metadataErr != nil {
		return nil, metadataErr
	}
	if !found || !hasPositions || !hasWeights || len(positions) != 3*len(weights) {
		return nil, errors.New("invalid SDK block metadata")
	}
	var out []block
	for i, weight := range weights {
		p := positions[3*i : 3*i+3]
		out = append(out, block{p[0], p[2] & 0xffff, p[1], p[2] >> 16, weight})
	}
	return out, nil
}

func contains(fn function, b block) bool {
	return (b.startLine > fn.start.Line || b.startLine == fn.start.Line && b.startCol >= fn.start.Column) &&
		(b.endLine < fn.end.Line || b.endLine == fn.end.Line && b.endCol <= fn.end.Column)
}

func checkScope(root, module, profile string, scope map[string][]string) (string, error) {
	if len(scope) == 0 {
		return "empty scope", errors.New("empty coverage scope")
	}
	var report strings.Builder
	f, err := os.Open(profile) //nolint:gosec // G304: the caller explicitly names the actual coverage profile to read.
	if err != nil {
		return "missing profile: " + err.Error(), err
	}
	hits, err := parseProfile(f)
	closeErr := closeProfile(f)
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "invalid profile: " + err.Error(), err
	}
	files := make([]string, 0, len(scope))
	for file := range scope {
		files = append(files, file)
	}
	sort.Strings(files)
	failed := false
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file))
		functions, err := sourceFunctions(path)
		if err != nil {
			fmt.Fprintf(&report, "%s: missing/unreadable source: %v\n", file, err)
			failed = true
			continue
		}
		blocks, err := sourceBlocks(path)
		if err != nil {
			fmt.Fprintf(&report, "%s: block inventory failed: %v\n", file, err)
			failed = true
			continue
		}
		required := scope[file]
		if len(required) > 0 {
			found := map[string]bool{}
			selected := []function{}
			for _, fn := range functions {
				for _, name := range required {
					if fn.name == name {
						found[name] = true
						selected = append(selected, fn)
					}
				}
			}
			for _, name := range required {
				if !found[name] {
					fmt.Fprintf(&report, "%s:%s: missing function\n", file, name)
					failed = true
				}
			}
			functions = selected
		}
		fileHits := hits[module+"/"+file]
		fileTotal, fileCovered := 0, 0
		for _, fn := range functions {
			total, covered := 0, 0
			for _, b := range blocks {
				if !contains(fn, b) || b.statements == 0 {
					continue
				}
				total += b.statements
				count, exists := fileHits[b]
				if count > 0 {
					covered += b.statements
				} else {
					reason := "zero-hit"
					if !exists {
						reason = "missing block"
					}
					fmt.Fprintf(&report, "%s:%s:%d.%d,%d.%d: %s (%d statements)\n", file, fn.name, b.startLine, b.startCol, b.endLine, b.endCol, reason, b.statements)
					failed = true
				}
			}
			if total > 0 {
				fmt.Fprintf(&report, "%s:%s: %d/%d statements %.1f%%\n", file, fn.name, covered, total, 100*float64(covered)/float64(total))
			}
			fileTotal, fileCovered = fileTotal+total, fileCovered+covered
		}
		if fileTotal > 0 {
			fmt.Fprintf(&report, "%s: FILE %d/%d statements %.1f%%\n", file, fileCovered, fileTotal, 100*float64(fileCovered)/float64(fileTotal))
		} else if len(required) == 0 {
			fmt.Fprintf(&report, "%s: no executable statements\n", file)
		}
	}
	if failed {
		return report.String(), errors.New("strict feature coverage failed")
	}
	return report.String(), nil
}

func nativeScope(root string) (map[string][]string, error) {
	cmd := exec.Command(goExe(), "list", "-json", "./internal/...") //nolint:gosec // G204: the exact build SDK with fixed arguments, no shell.
	cmd.Dir = root
	data, err := commandOutput(cmd)
	if err != nil {
		return nil, err
	}
	scope := map[string][]string{
		"internal/cli/mail.go":              nil,
		"internal/cli/calendar.go":          {"relatedMailOf"},
		"internal/cli/mail_source.go":       nil,
		"internal/cli/mail_state.go":        {"initializer:mailSupported", "runtime.mailStatusOf", "mailStatusBlock.text", "runtime.outlookProfiles", "outlookMailSource.profiles", "runtime.mailReadableCheck"},
		"internal/cli/doctor.go":            {"runtime.outlookStoreCheck"},
		"internal/cli/overview.go":          {"stepsHere", "overviewCmd.Run", "runtime.overviewMail", "runtime.whyNoMail"},
		"internal/cli/search_merge.go":      {"searchMailItemOf", "searchCmd.plan", "searchCmd.search"},
		"internal/cli/runtime.go":           {"runtime.checkOutlookRoot"},
		"internal/store/mail_owa.go":        nil,
		"internal/store/mail_source_ids.go": nil,
		"internal/store/mail_schema.go":     nil,
		"internal/store/mail_query.go":      nil,
		"internal/store/mail.go":            nil,
		"internal/syncer/outlook_owa.go":    nil,
		"internal/syncer/outlook.go":        nil,
		"internal/store/store.go":           {"Store.migrate", "Store.NeedsUpgrade"},
		"internal/store/schema.go":          nil,
		"internal/outlookmail/message.go":   nil,
		"scripts/coverage/main.go":          nil,
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	nativeOWA, foundOWA := false, false
	for {
		var pkg struct {
			Dir, ImportPath string
			GoFiles         []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if strings.HasSuffix(pkg.ImportPath, "/internal/outlookowa") {
			foundOWA = true
			for _, name := range pkg.GoFiles {
				rel, err := relativePath(root, filepath.Join(pkg.Dir, name))
				if err != nil {
					return nil, err
				}
				scope[filepath.ToSlash(rel)] = nil
				if name == "source_windows.go" {
					nativeOWA = true
				}
			}
		}
	}
	if !foundOWA || runtime.GOOS == "windows" && !nativeOWA {
		return nil, errors.New("native Windows OWA providers missing from scope")
	}
	return scope, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("coverage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profile := flags.String("profile", "", "actual native Go coverage profile")
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	absolute, err := absolutePath(*root)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	scope, err := coverageScope(absolute)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	report, err := checkScope(absolute, "github.com/ourostack/m365crawl", *profile, scope)
	if _, writeErr := fmt.Fprint(stdout, report); writeErr != nil {
		_, _ = fmt.Fprintln(stderr, "coverage report write failed:", writeErr)
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "strict native mail coverage OK: every required function and executable block is 100% covered"); err != nil {
		_, _ = fmt.Fprintln(stderr, "coverage result write failed:", err)
		return 1
	}
	return 0
}

func main() {
	exitProcess(run(os.Args[1:], os.Stdout, os.Stderr))
}
