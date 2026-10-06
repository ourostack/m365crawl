package teamscal

import (
	"testing"

	"github.com/ourostack/teamscrawl/internal/calendar"
)

func TestFlagDistinguishesAbsentFromFalse(t *testing.T) {
	m, err := decode([]byte(`{"yes":true,"no":false,"text":"TRUE","nul":null,"num":1,"word":"no","fstr":" False "}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key            string
		value, present bool
	}{
		{"yes", true, true}, {"no", false, true}, {"text", true, true},
		{"nul", false, false}, {"missing", false, false}, {"num", false, false}, {"word", false, false}, {"fstr", false, true},
	}
	for _, c := range cases {
		if v, p := flag(m, c.key); v != c.value || p != c.present {
			t.Errorf("flag(%s) = %v, %v; want %v, %v", c.key, v, p, c.value, c.present)
		}
		want := calendar.TriUnknown
		if c.present {
			want = calendar.TriOf(c.value)
		}
		if triFlag(m, c.key) != want {
			t.Errorf("triFlag(%s)", c.key)
		}
	}
}

func TestMapEventFlagsAbsentVersusFalse(t *testing.T) {
	absent, _ := mapEvent(t, "k", `{"iCalUID":"U"}`)
	explicit, _ := mapEvent(t, "k", `{"iCalUID":"U","isOrganizer":false,"isPrivate":false,"isCancelled":false,"isOnlineMeeting":false,"hasAttachments":false,"isAllDayEvent":false}`)
	if absent.IsOrganizer.Known() || absent.AllDay.Known() || !explicit.IsOrganizer.Is(false) || !explicit.AllDay.Is(false) || !explicit.Cancelled.Is(false) || !explicit.HasAttachments.Is(false) {
		t.Fatalf("flags: %+v / %+v", absent, explicit)
	}
}
