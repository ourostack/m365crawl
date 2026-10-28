package browser

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

// SelectedTestPage connects only the hermetic protocol server, without a browser process.
func SelectedTestPage(t *testing.T, s *browsertest.Server) *Page { return testPage(t, s) }
