package officecapture

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func ordinaryFixture(t *testing.T) (string, []byte) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("ordinary input qualification is Darwin/Windows only")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "recent.json")
	payload := []byte(`{"documents":{"items":[{"title":"Synthetic document","url":"https://example.invalid/document"}]}}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, payload
}

func TestJSONCapturePrivateCopyAndStableSource(t *testing.T) {
	path, payload := ordinaryFixture(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	err = WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"}, func(c *JSONCapture) error {
		private = c.Path
		if c.Path == path || c.ContentDigest != fmt.Sprintf("%x", sha256.Sum256(payload)) ||
			c.AcquisitionSignature == "" || c.AcquiredAt.IsZero() || !c.SourceModified.Equal(before.ModTime()) {
			t.Fatal("capture identity or clock missing")
		}
		data, err := os.ReadFile(c.Path)
		if err != nil || string(data) != string(payload) {
			t.Fatalf("private content mismatch: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		return c.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{private, filepath.Dir(private)} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("owned private path survived: %v", err)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("source identity or time modified")
	}
	actual, err := os.ReadFile(path) //nolint:gosec // Test-owned synthetic fixture.
	if err != nil || string(actual) != string(payload) {
		t.Fatal("source bytes modified")
	}
}

func TestJSONCaptureRejectsLinkedInputsBeforeConsumer(t *testing.T) {
	path, _ := ordinaryFixture(t)
	root := filepath.Dir(path)
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprint(ancestor), func(t *testing.T) {
			link := filepath.Join(root, fmt.Sprintf("link-%t", ancestor))
			target := path
			if ancestor {
				target = root
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("host does not permit synthetic links: %v", err)
			}
			input := link
			if ancestor {
				input = filepath.Join(link, "recent.json")
			}
			called := false
			err := WithJSONCapture(context.Background(), DocumentInput{Path: input, Surface: officedocuments.Recent, Application: "supplied"}, func(*JSONCapture) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatal("linked input admitted")
			}
		})
	}
}

func TestJSONCaptureLateFailureInvalidatesStagedResult(t *testing.T) {
	for _, failure := range []string{"cancel", "source-change", "private-change", "public-path", "panic", "consumer"} {
		t.Run(failure, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			outside := filepath.Join(filepath.Dir(path), "unrelated")
			if err := os.WriteFile(outside, []byte("do not remove"), 0o600); err != nil {
				t.Fatal(err)
			}
			var private string
			err := WithJSONCapture(ctx, DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"}, func(c *JSONCapture) error {
				private = c.Path
				switch failure {
				case "cancel":
					cancel()
				case "source-change":
					if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
						return err
					}
				case "private-change":
					if err := os.WriteFile(c.Path, []byte("changed"), 0o600); err != nil {
						return err
					}
				case "public-path":
					c.Path = outside
				case "panic":
					panic("private native value")
				case "consumer":
					return errors.New("consumer refused")
				}
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "private native value") {
				t.Fatalf("late failure escaped or was admitted: %v", err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
			if _, err := os.Lstat(private); !os.IsNotExist(err) {
				t.Fatalf("copy retained after refusal: %v", err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "do not remove" { //nolint:gosec // Test-owned unrelated sentinel.
				t.Fatal("mutable public path redirected cleanup")
			}
		})
	}
}

func TestJSONCaptureInvalidDescriptorNeverCallsConsumer(t *testing.T) {
	path, _ := ordinaryFixture(t)
	for _, input := range []DocumentInput{
		{Path: path, Surface: "unknown", Application: "supplied"},
		{Path: path, Surface: officedocuments.Recent, Application: "unknown"},
		{Path: "recent.json", Surface: officedocuments.Recent, Application: "supplied"},
	} {
		called := false
		err := WithJSONCapture(context.Background(), input, func(*JSONCapture) error {
			called = true
			return nil
		})
		if err == nil || called {
			t.Fatal("unqualified descriptor consumed")
		}
	}
}

func TestJSONObserveDoesNotReadSourceContent(t *testing.T) {
	path, _ := ordinaryFixture(t)
	if err := os.WriteFile(path, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	signature, err := ObserveJSON(context.Background(), path)
	if err != nil || signature == "" {
		t.Fatalf("metadata-only observation parsed input: %v", err)
	}
}

func TestJSONCaptureReaderUsesBoundPrivateInput(t *testing.T) {
	for _, change := range []string{"none", "closed", "public-path", "private-link", "private-replacement", "missing-envelope"} {
		t.Run(change, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			outside := filepath.Join(filepath.Dir(path), "outside.json")
			if err := os.WriteFile(outside, []byte(`{"documents":{"items":[{"title":"Wrong input","url":"https://example.invalid/wrong"}]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"}, func(c *JSONCapture) error {
				switch change {
				case "closed":
					if err := c.Close(); err != nil {
						return err
					}
				case "public-path":
					c.Path = outside
				case "private-link":
					if err := os.Remove(c.Path); err != nil {
						return err
					}
					if err := os.Symlink(outside, c.Path); err != nil {
						t.Skipf("host cannot create synthetic link: %v", err)
					}
				case "missing-envelope":
					if err := os.WriteFile(c.Path, []byte(`{}`), 0o600); err != nil {
						return err
					}
				case "private-replacement":
					if err := os.Remove(c.Path); err != nil {
						return err
					}
					if err := os.WriteFile(c.Path, []byte(`{"documents":{"items":[{"title":"Wrong input","url":"https://example.invalid/wrong"}]}}`), 0o600); err != nil {
						return err
					}
				}
				result, err := c.ReadJSON(context.Background())
				if change == "none" {
					if err != nil || len(result.Documents) != 1 || result.Documents[0].Title != "Synthetic document" {
						t.Fatalf("qualified private reader failed: %v", err)
					}
					return nil
				}
				if err == nil || len(result.Documents) != 0 {
					t.Fatal("unbound private input was decoded")
				}
				return err
			})
			if (change == "none") != (err == nil) {
				t.Fatalf("wrong capture outcome: %v", err)
			}
		})
	}
}

func TestJSONDiscoveryFiniteExplicitBindings(t *testing.T) {
	path, payload := ordinaryFixture(t)
	root := filepath.Dir(path)
	for _, name := range []string{"shared.json", "dialog.json", "unrelated.json"} {
		if err := os.WriteFile(filepath.Join(root, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := DiscoverJSON(context.Background(), root)
	if err != nil || len(inputs) != 3 {
		t.Fatalf("finite discovery failed: %v", err)
	}
	want := []officedocuments.Surface{officedocuments.Recent, officedocuments.Shared, officedocuments.Dialog}
	for i, input := range inputs {
		if input.Application != "supplied" || input.Surface != want[i] ||
			input.Path != filepath.Join(root, string(want[i])+".json") {
			t.Fatal("discovery inferred source or application")
		}
	}
}
