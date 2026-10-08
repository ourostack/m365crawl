package transcripts

import "testing"

func TestDecodeEntriesOffsets(t *testing.T) {
	raw := []RawEntry{
		{S: "Ada Example", B: "00:01:02.5000000", E: "00:01:04.25", T: "Hello."},
		{S: "Bo Example", B: "", E: "1:00:00", T: "No start."},
		{S: "", B: "12:34", E: "00:00:01.0009999", T: "Bad start."},
		{S: "Cy Example", B: "00:00:00", E: "00:60:00", T: "Bad end."},
		{S: "Cy Example", B: "00:00:61", E: "x", T: "Both bad."},
		{S: "Cy Example", B: "00:00:01.12345678", E: "-00:00:01", T: "Too many digits, a sign."},
	}
	got, bad := DecodeEntries(raw)
	if bad != 7 {
		t.Fatalf("bad offsets = %d, want 7", bad)
	}
	ms := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	want := []struct {
		speaker, text string
		start, end    any
	}{
		{"Ada Example", "Hello.", int64(62500), int64(64250)},
		{"Bo Example", "No start.", nil, int64(3_600_000)},
		{"", "Bad start.", nil, int64(1000)},
		{"Cy Example", "Bad end.", int64(0), nil},
		{"Cy Example", "Both bad.", nil, nil},
		{"Cy Example", "Too many digits, a sign.", nil, nil},
	}
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Speaker != w.speaker || g.Text != w.text || ms(g.StartMS) != w.start || ms(g.EndMS) != w.end {
			t.Errorf("entry %d = %q %q %v %v, want %+v", i, g.Speaker, g.Text, ms(g.StartMS), ms(g.EndMS), w)
		}
	}
	if got, bad := DecodeEntries(nil); len(got) != 0 || bad != 0 {
		t.Errorf("nil: %v %d", got, bad)
	}
}

func TestParseOffset(t *testing.T) {
	for in, want := range map[string]int64{
		"00:00:00":                   0,
		"00:01:02.5000000":           62500,
		"01:02:03.4":                 3_723_400,
		"10:00:00.0000001":           36_000_000,
		"123:00:00":                  442_800_000,
		"00:00:59.999":               59_999,
		"00:00:01.0005":              1000,
		"00:00:00.1234567":           123,
		"0:0:1":                      1000,
		"00:00:01.":                  -1,
		"00:00:01.5x":                -1,
		"00:00":                      -1,
		"aa:00:00":                   -1,
		"00:00:00.12345678":          -1,
		"00:61:00":                   -1,
		"":                           -1,
		"99999999999999999999:00:00": -1,
	} {
		got, ok := parseOffset(in)
		if want < 0 {
			if ok {
				t.Errorf("%q parsed as %d", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, ok, want)
		}
	}
}
