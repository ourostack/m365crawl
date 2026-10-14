package officecapture

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

func textFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native resident capture only on Darwin and Windows")
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, part := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Synthetic resident text</w:t></w:r></w:p></w:body></w:document>`},
	} {
		w, err := z.Create(part[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(part[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "synthetic.docx")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTextCaptureBoundNativeSnapshot(t *testing.T) {
	path := textFixture(t)
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	before, err := io.ReadAll(f)
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	var captured *TextCapture
	err = WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
		captured = c
		if c.SourceByteDigest != fmt.Sprintf("%x", sha256.Sum256(before)) || c.Evidence.Size != int64(len(before)) ||
			c.Evidence.GenerationPolicy != "native-file-incarnation-v1" || c.AcquiredAt.IsZero() {
			t.Fatal("incomplete native snapshot")
		}
		result, err := c.ReadText(context.Background())
		if err != nil {
			return err
		}
		if result.State != "text_observations" || len(result.Paragraphs) != 1 || result.Paragraphs[0].Text != "Synthetic resident text" {
			t.Fatalf("incorrect production projection: %#v", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captured.ReadText(context.Background()); err == nil {
		t.Fatal("reader usable after close")
	}
	for _, p := range []string{captured.privatePath, captured.root} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("private copy survived cleanup")
		}
	}
	if stamp, err := ObserveText(context.Background(), path); err != nil || stamp == "" {
		t.Fatalf("metadata retry: %q %v", stamp, err)
	}
}

func TestTextDeadlineIncludesGateWaiting(t *testing.T) {
	release, err := sourcecapture.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	err = WithTextCapture(ctx, TextInput{filepath.Join(filepath.VolumeName(os.Getenv("TMPDIR"))+string(filepath.Separator), "synthetic.docx"), "docx"}, func(*TextCapture) error {
		t.Fatal("opened while gate held")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate deadline lost: %v", err)
	}
	release()
	retry, err := sourcecapture.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retry()
}

func TestLocalRefusedFormatCannotReleaseRegistryLock(t *testing.T) {
	path := registrySource(t)
	err := WithRegistryCapture(context.Background(), RegistryInput{path}, func(*RegistryCapture) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		defer cancel()
		if err := WithTextCapture(ctx, TextInput{path, "docx"}, func(*TextCapture) error {
			t.Fatal("second descriptor lifetime admitted")
			return nil
		}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("gate not shared: %v", err)
		}
		if out := officeWriterProbe(t, path); !strings.Contains(out, "LOCKED") {
			t.Fatalf("source lock released: %s", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
		_, err := c.ReadText(context.Background())
		return err
	}); err == nil {
		t.Fatal("SQLite file admitted as OOXML")
	}
}

func TestTextEvidenceCanonicalClocksAndContinuity(t *testing.T) {
	modified := time.Unix(1, 2).UTC()
	for _, tc := range []struct {
		name               string
		gen, creation, raw *string
		kind               string
		qualified          bool
		bad                bool
	}{
		{"native", textString("7"), textString("9:0"), textString("1:2"), "native_generation", true, false},
		{"fallback", textString("0"), textString("9:0"), textString("1:2"), "native_creation", true, false},
		{"zeros", textString("0"), textString("0:0"), textString("1:2"), "unavailable", false, false},
		{"absent", nil, nil, textString("1:2"), "unavailable", false, false},
		{"noncanonical", textString("07"), textString("9:0"), textString("1:2"), "native_generation", true, true},
		{"nanoseconds", textString("7"), textString("9:1000000000"), textString("1:2"), "native_generation", true, true},
		{"clock mismatch", textString("7"), textString("9:0"), textString("1:3"), "native_generation", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := FileEvidence{VolumeID: "1", FileID: "2", GenerationPolicy: "native-file-incarnation-v1", NativeGenerationRaw: tc.gen,
				CreationRaw: tc.creation, ModificationRaw: tc.raw, GenerationKind: tc.kind, GenerationQualified: tc.qualified,
				Size: 1, ModifiedAt: &modified, NativeAttributes: "0"}
			if err := ValidateFileEvidence(e); (err != nil) != tc.bad {
				t.Fatalf("bad=%v err=%v", tc.bad, err)
			}
		})
	}
}

func textString(v string) *string { return &v }
