package browser

import (
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

func TestMain(m *testing.M) {
	browsertest.RunIfFake()
	os.Exit(m.Run())
}
