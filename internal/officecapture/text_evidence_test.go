package officecapture

import (
	"testing"
	"time"
)

func TestTextEvidenceRejectsContradictoryFields(t *testing.T) {
	clock := time.Unix(2, 0).UTC()
	base := FileEvidence{VolumeID: "1", FileID: "2", GenerationKind: "native_generation", GenerationPolicy: FileGenerationPolicy,
		NativeGenerationRaw: textString("7"), CreationRaw: textString("1:0"), ModificationRaw: textString("2:0"), ModifiedAt: &clock,
		GenerationQualified: true, Size: 8, NativeAttributes: "0"}
	for _, mutate := range []func(*FileEvidence){
		func(e *FileEvidence) { e.GenerationPolicy = "other" },
		func(e *FileEvidence) { e.Size = -1 },
		func(e *FileEvidence) { e.VolumeID = "01" },
		func(e *FileEvidence) { e.VolumeID = "0" },
		func(e *FileEvidence) { e.FileID = "0" },
		func(e *FileEvidence) { e.NativeGenerationRaw = textString("07") },
		func(e *FileEvidence) { e.CreationRaw = textString("1:1000000000") },
		func(e *FileEvidence) { e.ModificationRaw = textString("wrong") },
		func(e *FileEvidence) { e.GenerationKind = "unavailable" },
		func(e *FileEvidence) { e.ModificationRaw = nil },
		func(e *FileEvidence) { e.ModificationRaw = textString("0:0") },
		func(e *FileEvidence) { at := time.Unix(1<<62, 0); e.ModifiedAt = &at },
	} {
		e := base
		mutate(&e)
		if err := ValidateFileEvidence(e); err == nil {
			t.Fatal("contradictory evidence accepted")
		}
	}
	e := base
	e.ModificationRaw = nil
	e.ModifiedAt = nil
	if err := ValidateFileEvidence(e); err != nil {
		t.Fatal("absent clock refused")
	}
	e = base
	e.NativeGenerationRaw = nil
	e.GenerationKind = "native_creation"
	e.CreationRaw = textString("116444736000000001")
	e.ModificationRaw = textString("116444736020000000")
	if err := ValidatePlatformEvidence("windows", e); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlatformEvidence("darwin", e); err == nil {
		t.Fatal("clock domains conflated")
	}
	e.NativeGenerationRaw = textString("0")
	if err := ValidatePlatformEvidence("windows", e); err == nil {
		t.Fatal("Windows generation invented")
	}
	if err := ValidatePlatformEvidence("other", base); err == nil {
		t.Fatal("unknown platform")
	}
	if err := ValidatePlatformEvidence("darwin", base); err != nil {
		t.Fatal(err)
	}
	e.VolumeID = "bad"
	if err := ValidatePlatformEvidence("windows", e); err == nil {
		t.Fatal("native validation bypassed")
	}
}

func TestTextRegistrationPortableExactTuples(t *testing.T) {
	for _, tc := range []struct {
		path, platform, kind string
		valid                bool
	}{
		{"/synthetic/a.docx", "darwin", "docx", true},
		{`C:\synthetic\a.docx`, "windows", "docx", true},
		{`C:\synthetic\a:stream.docx`, "windows", "docx", false},
		{`C:\synthetic\..\a.docx`, "windows", "docx", false},
		{`C:\synthetic\\a.docx`, "windows", "docx", false},
		{`C:\synthetic\a.docx `, "windows", "docx", false},
		{"relative", "darwin", "docx", false},
		{"/a", "other", "docx", false},
		{"/a", "darwin", "pdf", false},
		{"/a\x00b", "darwin", "docx", false},
	} {
		if ValidTextRegistration(TextInput{tc.path, tc.kind}, tc.platform) != tc.valid {
			t.Fatalf("native tuple %q", tc.path)
		}
	}
}
