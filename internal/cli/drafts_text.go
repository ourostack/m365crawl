package cli

import (
	"encoding/json"
	"fmt"
)

func (r *draftsResult) renderDrafts(rt *runtime) error {
	if _, err := fmt.Fprintln(rt.stdout, "Own cached compose-body projections (not current unsent messages)"); err != nil {
		return err
	}
	enc := json.NewEncoder(rt.stdout)
	for _, item := range r.Items {
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	if err := enc.Encode(struct {
		Count      int  `json:"count"`
		Truncated  bool `json:"truncated"`
		TotalExact *int `json:"total_exact,omitempty"`
	}{r.Count, r.Truncated, r.TotalExact}); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(rt.stdout, draftCacheWarning+"\ndraft_coverage (independent of item fields and preview limits):"); err != nil {
		return err
	}
	return enc.Encode(r.DraftCoverage)
}
