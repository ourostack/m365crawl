package cli

import "testing"

func TestEngageSigninRefusesEmptyURLComponents(t *testing.T) {
	for _, target := range []string{"https://engage.cloud.microsoft/?", "https://engage.cloud.microsoft/#", "https://engage.cloud.microsoft:443/#"} {
		if engageSigninTarget(target) {
			t.Fatalf("raw empty query/fragment admitted: %s", target)
		}
	}
}
