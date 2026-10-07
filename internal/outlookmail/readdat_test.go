package outlookmail

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeDat(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, "Files", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

const datPath = "~/Files/S0/2/EFMData/1.dat"

func TestReadDat(t *testing.T) {
	root := t.TempDir()
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, []byte("<html>fixture</html>")))
	got, err := ReadDat(root, datPath, MaxBodyBytes)
	if err != nil || string(got) != "<html>fixture</html>" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestReadDatCap(t *testing.T) {
	root := t.TempDir()
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, make([]byte, 17<<20)))
	if _, err := ReadDat(root, datPath, MaxBodyBytes); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("17 MiB of zeros: %v", err)
	}
	writeDat(t, root, "S0/2/EFMData/2.dat", gz(t, make([]byte, 10)))
	if b, err := ReadDat(root, "~/Files/S0/2/EFMData/2.dat", 10); err != nil || len(b) != 10 {
		t.Fatalf("exactly at the cap: %d %v", len(b), err)
	}
	if _, err := ReadDat(root, "~/Files/S0/2/EFMData/2.dat", 9); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("one over the cap: %v", err)
	}
}

func TestMaxBodyBytes(t *testing.T) {
	if MaxBodyBytes != 16<<20 {
		t.Fatal(MaxBodyBytes)
	}
}

func TestReadDatBadFiles(t *testing.T) {
	root := t.TempDir()
	full := gz(t, bytes.Repeat([]byte("<p>fixture</p>"), 1000))
	writeDat(t, root, "a/truncated.dat", full[:len(full)/2])
	writeDat(t, root, "a/plain.dat", []byte("<html>not gzip</html>"))
	writeDat(t, root, "a/one.dat", []byte{0x1f})
	writeDat(t, root, "a/empty.dat", nil)
	writeDat(t, root, "a/badheader.dat", []byte{0x1f, 0x8b, 0x00, 0x00})
	if err := os.MkdirAll(filepath.Join(root, "Files", "a", "dir.dat"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"truncated", "plain", "one", "empty", "badheader", "dir"} {
		_, err := ReadDat(root, "~/Files/a/"+name+".dat", MaxBodyBytes)
		if err == nil || errors.Is(err, ErrBodyMissing) || errors.Is(err, ErrBodyTooLarge) || errors.Is(err, ErrBodyPath) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestReadDatTruncated(t *testing.T) {
	root := t.TempDir()
	full := gz(t, bytes.Repeat([]byte("<p>fixture</p>"), 1000))
	writeDat(t, root, "S0/2/EFMData/1.dat", full[:len(full)-8])
	if _, err := ReadDat(root, datPath, MaxBodyBytes); !errors.Is(err, ErrBodyFormat) {
		t.Fatalf("%v", err)
	}
}

func TestReadDatMissing(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadDat(root, datPath, MaxBodyBytes); !errors.Is(err, ErrBodyMissing) {
		t.Fatalf("no Files directory: %v", err)
	}
	writeDat(t, root, "S0/2/EFMData/other.dat", gz(t, []byte("x")))
	if _, err := ReadDat(root, datPath, MaxBodyBytes); !errors.Is(err, ErrBodyMissing) {
		t.Fatalf("no such file: %v", err)
	}
	// The file vanishes between the path check and the open.
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, []byte("x")))
	old := openDat
	defer func() { openDat = old }()
	openDat = func(string) (*os.File, error) { return nil, fs.ErrNotExist }
	if _, err := ReadDat(root, datPath, MaxBodyBytes); !errors.Is(err, ErrBodyMissing) {
		t.Fatalf("vanished: %v", err)
	}
	openDat = func(string) (*os.File, error) { return nil, fs.ErrPermission }
	if _, err := ReadDat(root, datPath, MaxBodyBytes); err == nil || errors.Is(err, ErrBodyMissing) {
		t.Fatalf("permission: %v", err)
	}
}

func TestReadDatResolveFailure(t *testing.T) {
	// Files exists but cannot be resolved for a reason other than absence: a path component
	// that is a file.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Files"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDat(root, datPath, MaxBodyBytes); err == nil || (runtime.GOOS != "windows" && errors.Is(err, ErrBodyMissing)) {
		t.Fatalf("%v", err) // Windows reports a path through a file as not found
	}
	// The profile directory itself does not exist.
	if _, err := ReadDat(filepath.Join(root, "none"), datPath, MaxBodyBytes); !errors.Is(err, ErrBodyMissing) {
		t.Fatalf("%v", err)
	}
}

func TestReadDatOutsideRoot(t *testing.T) {
	root := t.TempDir()
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, []byte("x")))
	outside := filepath.Join(filepath.Dir(root), "outside.dat")
	if err := os.WriteFile(outside, gz(t, []byte("x")), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(outside) }()
	for _, p := range []string{
		"~/Files/../../x", "~/Files/S0/../../outside.dat", "~/Files/./x", "~/Files//x", "~/Files/", "~/Files",
		"/etc/hosts", "Files/S0/2/EFMData/1.dat", "S0/2/EFMData/1.dat", "", `~/Files/S0\2\EFMData\1.dat`, `~\Files\x`,
		"C:/Files/x", "~/Files/C:/x", `\\host\share\x`, "//host/share/x", "~/Files/a\x00b", "~/Files/..",
	} {
		if _, err := ReadDat(root, p, MaxBodyBytes); !errors.Is(err, ErrBodyPath) {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestReadDatSymlinkOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "1.dat"), gz(t, []byte("x")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "Files", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "1.dat"), filepath.Join(root, "Files", "file.dat")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"~/Files/link/1.dat", "~/Files/file.dat"} {
		if _, err := ReadDat(root, p, MaxBodyBytes); !errors.Is(err, ErrBodyPath) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// A link that stays inside Files is fine.
	writeDat(t, root, "real/1.dat", gz(t, []byte("inside")))
	if err := os.Symlink(filepath.Join(root, "Files", "real"), filepath.Join(root, "Files", "alias")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadDat(root, "~/Files/alias/1.dat", MaxBodyBytes); err != nil || string(b) != "inside" {
		t.Fatalf("%q %v", b, err)
	}
}
