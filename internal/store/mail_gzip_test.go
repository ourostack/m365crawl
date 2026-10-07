package store

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// The pooled compressors must produce what a fresh writer produces, for every caller at once.
func TestBodyGzipMatchesAFreshWriter(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				html := []byte(strings.Repeat(fmt.Sprintf("<p>caller %d message %d</p>", g, i), 20+g*i))
				d := mailDerived{hasHTML: true}
				d.body(outlookmail.Message{Body: outlookmail.Body{HTML: html}})
				var want bytes.Buffer
				zw := gzip.NewWriter(&want)
				_, _ = zw.Write(html)
				_ = zw.Close()
				if !bytes.Equal(d.gz, want.Bytes()) {
					t.Errorf("caller %d message %d: pooled output differs from a fresh writer", g, i)
					return
				}
			}
		}()
	}
	wg.Wait()
}
