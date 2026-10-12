//go:build acceptance

package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRealOfficeJSONGuardedCapture(t *testing.T) {
	if os.Getenv("M365CRAWL_REAL_CACHE") != "1" {
		t.Skip("set M365CRAWL_REAL_CACHE=1 for real source qualification")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("named native Office sources are Mac only")
	}
	for cycle := range 2 {
		inputs, err := DiscoverJSON(context.Background(), "")
		if err != nil {
			t.Fatal("native named source discovery refused")
		}
		recognized, refused, rows := 0, 0, 0
		for _, input := range inputs {
			var private string
			decoded := false
			err := WithJSONCapture(context.Background(), input, func(c *JSONCapture) error {
				private = c.Path
				result, err := c.ReadJSON(context.Background())
				if err != nil {
					return err
				}
				decoded = true
				rows += len(result.Documents)
				return nil
			})
			if err != nil {
				refused++
			} else {
				if !decoded {
					t.Fatal("capture success without an admitted source envelope")
				}
				recognized++
			}
			if private != "" {
				for _, path := range []string{private, filepath.Dir(private)} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("private source copy survived terminal capture")
					}
				}
			}
		}
		if recognized == 0 || len(inputs) > 128 {
			t.Fatal("no qualified native source or input bound exceeded")
		}
		t.Logf("cycle=%d sources=%d recognized=%d refused=%d rows=%d copies_removed=true", cycle+1, len(inputs), recognized, refused, rows)
	}
}
