package engagecontent

import (
	"context"
	"strings"
	"testing"
)

func TestEngageFinalOptionalAliasAccounting(t *testing.T) {
	r := finalFixture(t)
	value := strings.Repeat("x", 262144)
	r.Threads[0].SenderID = &value
	r.Threads[0].Title = &value
	r.Threads[0].Blocks = []string{value, value, value}
	if err := ValidateResult(context.Background(), r); err != nil {
		t.Fatal("pointer alias incorrectly counted sender as text:", err)
	}
}
