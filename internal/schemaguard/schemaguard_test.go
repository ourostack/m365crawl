package schemaguard

import (
	"strings"
	"testing"
)

func store(schema string, features ...string) map[string][]byte {
	src := "package store\n\nconst SchemaVersion = " + schema + "\n"
	for _, f := range features {
		src += "const " + f + "\n"
	}
	return map[string][]byte{"schema.go": []byte(src)}
}

func TestVersionsReadsTheLiteralConstants(t *testing.T) {
	files := store("28", "WorkIQSchemaVersion = 22", "DirectedDraftSchemaVersion = 28")
	files["other.go"] = []byte("package store\n\nconst (\n\tA, BSchemaVersion = 1, 26\n\tunrelated = 3\n\tcountVersion = 4\n)\n\nvar CSchemaVersion = 9\n")
	v, problems := Versions(files)
	if len(problems) != 0 || v.Schema != 28 || len(v.Features) != 3 || v.Features["WorkIQSchemaVersion"] != 22 ||
		v.Features["DirectedDraftSchemaVersion"] != 28 || v.Features["BSchemaVersion"] != 26 {
		t.Fatalf("versions = %+v, %v", v, problems)
	}
}

func TestVersionsReportsWhatItCannotRead(t *testing.T) {
	for name, files := range map[string]map[string][]byte{
		"not Go":           {"x.go": []byte("package")},
		"no SchemaVersion": {"x.go": []byte("package store\n")},
		"computed":         store("28", "XSchemaVersion = SchemaVersion + 1"),
		"not an integer":   store(`"28"`),
		"too large":        store("99999999999999999999"),
		"no value":         {"x.go": []byte("package store\n\nconst (\n\tSchemaVersion = 3\n\tYSchemaVersion\n)\n")},
	} {
		if _, problems := Versions(files); len(problems) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
}

func TestCheckHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		head map[string][]byte
		want string
	}{
		{"ok", store("29", "ASchemaVersion = 22", "BSchemaVersion = 29"), ""},
		{"duplicate", store("29", "ASchemaVersion = 26", "BSchemaVersion = 26"), "ASchemaVersion and BSchemaVersion are both 26"},
		{"above SchemaVersion", store("28", "ASchemaVersion = 29"), "ASchemaVersion is 29, above SchemaVersion 28"},
	} {
		got := strings.Join(Check(nil, tc.head), "\n")
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s = %q", tc.name, got)
		}
	}
}

func TestCheckAgainstTheBase(t *testing.T) {
	base := store("28", "ASchemaVersion = 26", "BSchemaVersion = 28")
	for _, tc := range []struct {
		name string
		head map[string][]byte
		want []string
	}{
		{"unchanged", base, nil},
		{"a new number above the base", store("29", "ASchemaVersion = 26", "BSchemaVersion = 28", "CSchemaVersion = 29"), nil},
		{"a new feature at a number main already used", store("29", "ASchemaVersion = 26", "BSchemaVersion = 28", "CSchemaVersion = 27"),
			[]string{"CSchemaVersion is 27, not above the base branch's SchemaVersion 28"}},
		{"a moved feature", store("29", "ASchemaVersion = 26", "BSchemaVersion = 25"),
			[]string{"BSchemaVersion is 25, not above the base branch's SchemaVersion 28"}},
		{"a removed feature and a lower SchemaVersion", store("27", "ASchemaVersion = 26"),
			[]string{"SchemaVersion is 27, below the base branch's 28", "BSchemaVersion (28 on the base branch) was removed"}},
		{"a raised SchemaVersion alone", store("29", "ASchemaVersion = 26", "BSchemaVersion = 28"), nil},
	} {
		got := Check(base, tc.head)
		ok := len(got) == len(tc.want)
		for i := 0; ok && i < len(got); i++ {
			ok = strings.HasPrefix(got[i], tc.want[i])
		}
		if !ok {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Check(map[string][]byte{"x.go": []byte("package")}, base); len(got) != 1 || !strings.HasPrefix(got[0], "base branch: ") {
		t.Errorf("unreadable base = %q", got)
	}
	if got := Check(base, map[string][]byte{"x.go": []byte("package store\n")}); len(got) != 1 || strings.HasPrefix(got[0], "base branch: ") {
		t.Errorf("unreadable head = %q", got)
	}
}
