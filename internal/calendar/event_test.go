package calendar

import (
	"testing"
	"time"
)

func mustTime(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func tp(t testing.TB, s string) *time.Time {
	v := mustTime(t, s)
	return &v
}

func TestFormatTime(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		t.Fatalf("zero time = %q, want empty", got)
	}
	in := time.Date(2026, 10, 5, 9, 30, 0, 0, time.FixedZone("x", -7*3600))
	if got, want := formatTime(in), "2026-10-05T16:30:00.000Z"; got != want {
		t.Fatalf("formatTime = %q, want %q", got, want)
	}
	if got := formatTimePtr(nil); got != nil {
		t.Fatalf("nil ptr = %v, want nil", got)
	}
	if got := formatTimePtr(&in); got != "2026-10-05T16:30:00.000Z" {
		t.Fatalf("ptr = %v", got)
	}
}

func TestTimeTextScan(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    time.Time
		wantErr bool
	}{
		{"nil", nil, time.Time{}, false},
		{"empty", "", time.Time{}, false},
		{"value", "2026-10-05T16:30:00.000Z", time.Date(2026, 10, 5, 16, 30, 0, 0, time.UTC), false},
		{"not text", int64(5), time.Time{}, true},
		{"bad text", "yesterday", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s timeText
			err := s.Scan(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !err2bool(err) && !s.t.Equal(tt.want) {
				t.Fatalf("t = %v, want %v", s.t, tt.want)
			}
			if tt.in == nil && s.ptr() != nil {
				t.Fatal("nil input must give a nil pointer")
			}
			if tt.name == "value" && (s.ptr() == nil || !s.ptr().Equal(tt.want)) {
				t.Fatal("ptr mismatch")
			}
		})
	}
}

func err2bool(err error) bool { return err != nil }
