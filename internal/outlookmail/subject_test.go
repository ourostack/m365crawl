package outlookmail

import "testing"

func TestNormalizeSubjectNativeContract(t *testing.T) {
	for in, want := range map[string]string{
		"RE: Fw: x": "x", "AW: x": "x", "Re:Re: x": "x", "plain": "plain", "  Fwd :  Wg: SV: y ": "y", "Re": "Re", "Reply: x": "Reply: x",
		"": "", "Re: ": "", "fw:re: Mixed Case": "Mixed Case", "Ünï: x": "Ünï: x", "S": "S",
	} {
		if got := NormalizeSubject(in); got != want {
			t.Fatal("shared native subject normalization changed")
		}
	}
}
