package engagecontent

import (
	"context"
	"time"
)

// MaxResultJSONBytes bounds stored canonical evidence before it is decoded.
// It exceeds the retained-model envelope without increasing producer budgets.
const MaxResultJSONBytes = 64 << 20

// ValidateResult checks final, owned evidence without repairing or mutating it.
// Transport-only account fragments are deliberately not reconstructed here.
func ValidateResult(ctx context.Context, r Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	malformed := &ReadError{Code: "malformed"}
	if r.Complete || r.Qualification != "native_home_observations" ||
		(r.State != "observations" && r.State != "metadata_only") {
		return malformed
	}
	if len(r.Threads) > 128 || len(r.Losses) > len(lossCodes) {
		return &ReadError{Code: "too_large"}
	}
	v := validator{}
	for _, s := range []string{r.Account.Host, r.Account.NetworkID, r.Account.UserID} {
		if err := v.string(s, false, false); err != nil {
			return err
		}
	}
	if r.Account.Host != "engage.cloud.microsoft" {
		return malformed
	}
	last, missingContent, clockLoss := "", false, 0
	for _, loss := range r.Losses {
		if !lossCodes[loss.Code] || loss.Code <= last || loss.Count < 1 || loss.Count > 65536 {
			return malformed
		}
		last = loss.Code
		if loss.Code == "native_content_not_observed" {
			missingContent = true
		}
		if loss.Code == "clock_unmapped" {
			clockLoss = loss.Count
		}
	}
	if (r.State == "observations" && len(r.Threads) == 0) ||
		(r.State == "metadata_only" && (len(r.Threads) != 0 || !missingContent)) {
		return malformed
	}
	blocks, unknownClocks := 0, 0
	for i, thread := range r.Threads {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(thread.Blocks) > 4096 || blocks+len(thread.Blocks) > 65536 {
			return &ReadError{Code: "too_large"}
		}
		blocks += len(thread.Blocks)
		if thread.Ordinal != i || thread.NetworkID != r.Account.NetworkID {
			return malformed
		}
		for _, s := range []string{thread.ID, thread.NetworkID, thread.GroupID, thread.StarterID} {
			if err := v.string(s, false, false); err != nil {
				return err
			}
		}
		for _, clock := range []struct {
			raw *string
			at  *time.Time
		}{
			{thread.CreatedRaw, thread.CreatedAt}, {thread.UpdatedRaw, thread.UpdatedAt},
			{thread.StarterCreatedRaw, thread.StarterCreatedAt}, {thread.StarterUpdatedRaw, thread.StarterUpdatedAt},
		} {
			if clock.raw != nil {
				if err := v.string(*clock.raw, false, true); err != nil {
					return err
				}
			}
			if err := validateClock(clock.raw, clock.at); err != nil {
				return err
			}
			if clock.at == nil {
				unknownClocks++
			}
		}
		for fieldIndex, s := range []*string{thread.SenderID, thread.Language, thread.Title} {
			if s != nil {
				if err := v.string(*s, fieldIndex == 2, true); err != nil {
					return err
				}
			}
		}
		if thread.Version != nil && (*thread.Version < 0 || *thread.Version > 9007199254740991) {
			return malformed
		}
		for _, s := range thread.Blocks {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := v.string(s, true, true); err != nil {
				return err
			}
		}
	}
	if unknownClocks > clockLoss {
		return malformed
	}
	return ctx.Err()
}

func validateClock(raw *string, at *time.Time) error {
	malformed := &ReadError{Code: "malformed"}
	if raw == nil {
		if at != nil {
			return malformed
		}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		if at != nil {
			return malformed
		}
		return nil
	}
	if at == nil {
		return malformed
	}
	want, err := parsed.MarshalJSON()
	if err != nil {
		return malformed
	}
	got, err := at.MarshalJSON()
	if err != nil || string(got) != string(want) || !at.Equal(parsed) {
		return malformed
	}
	return nil
}
