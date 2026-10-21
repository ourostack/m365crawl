//go:build acceptance

package acceptance

import (
	"errors"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func owaCensusCloseReader(origin *indexeddb.Origin) (result error) {
	defer func() {
		if recover() != nil {
			result = errors.New("census_close_refused")
		}
	}()
	return owaCensusClose(origin)
}
