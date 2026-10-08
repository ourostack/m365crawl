package store

import (
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/transcripts"
)

// TestMain lets the synthetic SharePoint hosts of the fixtures, under the reserved .invalid
// domain, stand in for real ones.
func TestMain(m *testing.M) {
	transcripts.AllowHostSuffixForTests(".sharepoint.example.invalid")
	os.Exit(m.Run())
}
