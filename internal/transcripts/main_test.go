package transcripts

import (
	"os"
	"testing"
)

// TestMain lets the synthetic hosts of the fixtures, under the reserved .invalid domain, stand in
// for SharePoint hosts.
func TestMain(m *testing.M) {
	AllowHostSuffixForTests(testSuffix)
	os.Exit(m.Run())
}

const testSuffix = ".sharepoint.example.invalid"
