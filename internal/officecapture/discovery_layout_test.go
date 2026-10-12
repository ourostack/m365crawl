//go:build darwin || windows

package officecapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func officeHomeFixture(t *testing.T, accounts int) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []struct{ name, prefix string }{{"Word", "w"}, {"Excel", "x"}, {"Powerpoint", "p"}} {
		office := filepath.Join(home, "Library", "Containers", "com.microsoft."+app.name,
			"Data", "Library", "Application Support", "Microsoft", "Office", "16.0")
		for i := range accounts {
			recent := filepath.Join(office, "aggmru", fmt.Sprintf("account-%03d", i))
			if err := os.MkdirAll(recent, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"mru4", "swm", "rec"} {
				if err := os.WriteFile(filepath.Join(recent, app.prefix+"-"+suffix+"-en-US-sr.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		dialog := filepath.Join(office, "MruServiceCache", "account", "cache")
		if err := os.MkdirAll(dialog, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dialog, "Documents_en-US"), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestJSONDiscoveryNamedDefaultsAreSourceDescriptorsOnly(t *testing.T) {
	home := officeHomeFixture(t, 1)
	getHome, mac := discoveryHome, discoveryMac
	t.Cleanup(func() { discoveryHome, discoveryMac = getHome, mac })
	discoveryMac = true
	discoveryHome = func() (string, error) { return home, nil }
	inputs, err := DiscoverJSON(context.Background(), "")
	if err != nil || len(inputs) != 12 {
		t.Fatalf("named default discovery failed: %v", err)
	}
	for _, input := range inputs {
		if input.Application == "supplied" || !validDocumentInput(input) {
			t.Fatal("default application binding lost")
		}
	}
	if inputs[0].Application != "Word" || inputs[0].Surface != "recent" ||
		inputs[8].Application != "PowerPoint" {
		t.Fatal("native descriptor binding changed")
	}
}

func TestJSONDiscoveryMissingUnsafeAndBoundedInputs(t *testing.T) {
	for _, fault := range []string{"empty", "home", "account", "dialog", "read", "names", "candidates", "cancel", "relative", "close"} {
		t.Run(fault, func(t *testing.T) {
			accounts := 1
			if fault == "candidates" {
				accounts = 44
			}
			home := officeHomeFixture(t, accounts)
			getHome, read, closeFile, mac := discoveryHome, discoveryRead, jsonClose, discoveryMac
			t.Cleanup(func() { discoveryHome, discoveryRead, jsonClose, discoveryMac = getHome, read, closeFile, mac })
			discoveryMac = true
			discoveryHome = func() (string, error) {
				if fault == "home" {
					return "", os.ErrPermission
				}
				if fault == "empty" {
					return filepath.Join(home, "missing"), nil
				}
				return home, nil
			}
			switch fault {
			case "account":
				base := filepath.Join(home, "Library", "Containers", "com.microsoft.Word", "Data", "Library",
					"Application Support", "Microsoft", "Office", "16.0", "aggmru")
				if err := os.WriteFile(filepath.Join(base, "wrong-type"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dialog":
				base := filepath.Join(home, "Library", "Containers", "com.microsoft.Word", "Data", "Library",
					"Application Support", "Microsoft", "Office", "16.0", "MruServiceCache", "account")
				if err := os.WriteFile(filepath.Join(base, "wrong-type"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "read":
				discoveryRead = func(*os.File, int) ([]string, error) { return nil, os.ErrPermission }
			case "names":
				discoveryRead = func(*os.File, int) ([]string, error) { return make([]string, 129), io.EOF }
			case "close":
				jsonClose = func(f *os.File) error { return errors.Join(closeFile(f), os.ErrPermission) }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := ""
			if fault == "cancel" {
				cancel()
			}
			if fault == "relative" {
				root = "unqualified"
			}
			if inputs, err := DiscoverJSON(ctx, root); err == nil || inputs != nil {
				t.Fatal("failed discovery returned success-shaped descriptors")
			}
		})
	}
}
