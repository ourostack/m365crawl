package engagecontent

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var lossCodes = map[string]bool{
	"thread_fragment_unmapped": true, "foreign_network_unmapped": true, "native_content_not_observed": true,
	"graphql_error": true, "references_unmapped": true, "formatting_unmapped": true,
	"attachments_unmapped": true, "replies_unmapped": true, "optional_field_unmapped": true,
	"clock_unmapped": true, "version_unmapped": true, "flag_unmapped": true,
	"viewer_fragment_unmapped": true,
}

func decode(ctx context.Context, raw scriptResult) (Result, error) {
	refuse := func(code string) (Result, error) { return Result{}, &ReadError{Code: code} }
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if raw.Fatal != "" {
		switch raw.Fatal {
		case "too_large", "malformed", "identity_missing", "identity_drift", "response_failed", "cleanup_failed", "capture_incomplete", "document_changed", "already_stopped", "observer_missing":
			return refuse(raw.Fatal)
		default:
			return refuse("malformed")
		}
	}
	if raw.State != "observations" && raw.State != "metadata_only" {
		return refuse("malformed")
	}
	if len(raw.Threads) > 128 || len(raw.AccountEvidence) > 128 || len(raw.ViewerFragments) > 128 || len(raw.Losses) > len(lossCodes) {
		return refuse("too_large")
	}
	v := validator{losses: map[string]int{}}
	for _, field := range []string{raw.Account.Host, raw.Account.NetworkID, raw.Account.UserID} {
		if err := v.string(field, false, true); err != nil {
			return Result{}, err
		}
	}
	if raw.Account.Host != "engage.cloud.microsoft" || raw.Account.NetworkID == "" || raw.Account.UserID == "" || len(raw.AccountEvidence) == 0 {
		return refuse("identity_missing")
	}
	for _, account := range raw.AccountEvidence {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		for _, field := range []string{account.Host, account.NetworkID, account.UserID} {
			if err := v.string(field, false, true); err != nil {
				return Result{}, err
			}
		}
		if account != raw.Account {
			return refuse("identity_drift")
		}
	}
	for _, fragment := range raw.ViewerFragments {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		for _, field := range []string{fragment.Host, fragment.UserID, fragment.NetworkID} {
			if err := v.string(field, false, true); err != nil {
				return Result{}, err
			}
		}
		if fragment.Host != raw.Account.Host || (fragment.UserID != "" && fragment.UserID != raw.Account.UserID) ||
			(fragment.NetworkID != "" && fragment.NetworkID != raw.Account.NetworkID) {
			return refuse("identity_drift")
		}
	}
	for _, loss := range raw.Losses {
		if !lossCodes[loss.Code] || loss.Count < 1 || loss.Count > 65536 || v.losses[loss.Code] != 0 {
			return refuse("malformed")
		}
		v.losses[loss.Code] = loss.Count
	}
	if (raw.State == "observations" && len(raw.Threads) == 0) || (raw.State == "metadata_only" && (len(raw.Threads) != 0 || v.losses["native_content_not_observed"] == 0)) {
		return refuse("malformed")
	}
	blocks := 0
	for _, thread := range raw.Threads {
		if len(thread.Blocks) > 4096 || blocks+len(thread.Blocks) > 65536 {
			return refuse("too_large")
		}
		blocks += len(thread.Blocks)
	}
	result := Result{State: raw.State, Qualification: "native_home_observations", Account: raw.Account}
	for i, thread := range raw.Threads {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if thread.Ordinal != i || thread.NetworkID != raw.Account.NetworkID {
			return refuse("malformed")
		}
		for _, field := range []string{thread.ID, thread.NetworkID, thread.GroupID, thread.StarterID} {
			if err := v.string(field, false, false); err != nil {
				return Result{}, err
			}
		}
		out := ThreadObservation{Ordinal: i, ID: thread.ID, NetworkID: thread.NetworkID, GroupID: thread.GroupID, StarterID: thread.StarterID}
		var err error
		if out.CreatedRaw, out.CreatedAt, err = v.clock(thread.CreatedRaw); err != nil {
			return Result{}, err
		}
		if out.UpdatedRaw, out.UpdatedAt, err = v.clock(thread.UpdatedRaw); err != nil {
			return Result{}, err
		}
		if out.StarterCreatedRaw, out.StarterCreatedAt, err = v.clock(thread.StarterCreatedRaw); err != nil {
			return Result{}, err
		}
		if out.StarterUpdatedRaw, out.StarterUpdatedAt, err = v.clock(thread.StarterUpdatedRaw); err != nil {
			return Result{}, err
		}
		if out.SenderID, err = v.optionalString(thread.SenderID, false); err != nil {
			return Result{}, err
		}
		if out.Language, err = v.optionalString(thread.Language, false); err != nil {
			return Result{}, err
		}
		if out.Title, err = v.optionalString(thread.Title, true); err != nil {
			return Result{}, err
		}
		if out.Version, err = v.version(thread.Version); err != nil {
			return Result{}, err
		}
		if out.IsDeleted, err = v.flag(thread.IsDeleted); err != nil {
			return Result{}, err
		}
		if out.IsDraft, err = v.flag(thread.IsDraft); err != nil {
			return Result{}, err
		}
		for _, text := range thread.Blocks {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if err := v.string(text, true, true); err != nil {
				return Result{}, err
			}
		}
		out.Blocks = append([]string(nil), thread.Blocks...)
		result.Threads = append(result.Threads, out)
	}
	for code, count := range v.losses {
		result.Losses = append(result.Losses, Loss{Code: code, Count: count})
	}
	sort.Slice(result.Losses, func(i, j int) bool { return result.Losses[i].Code < result.Losses[j].Code })
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

type validator struct {
	stringBytes, textBytes int
	losses                 map[string]int
}

func (v *validator) string(value string, text, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || (!allowEmpty && value == "") {
		return &ReadError{Code: "malformed"}
	}
	if len(value) > 262144 || v.stringBytes+len(value) > 8388608 || (text && v.textBytes+len(value) > 1048576) {
		return &ReadError{Code: "too_large"}
	}
	v.stringBytes += len(value)
	if text {
		v.textBytes += len(value)
	}
	return nil
}

func (v *validator) optionalString(raw json.RawMessage, text bool) (*string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		v.losses["optional_field_unmapped"]++
		return nil, nil
	}
	if len(raw) > 2097152 || !json.Valid(raw) || !utf8.Valid(raw) {
		return nil, &ReadError{Code: "malformed"}
	}
	if raw[0] == '"' && !validStringScalar(raw) {
		return nil, &ReadError{Code: "malformed"}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		v.losses["optional_field_unmapped"]++
		return nil, nil
	}
	if err := v.string(value, text, true); err != nil {
		return nil, err
	}
	return &value, nil
}

func (v *validator) clock(raw json.RawMessage) (*string, *time.Time, error) {
	value, err := v.optionalString(raw, false)
	if err != nil {
		return nil, nil, err
	}
	if value == nil {
		v.losses["clock_unmapped"]++
		return nil, nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		v.losses["clock_unmapped"]++
		return value, nil, nil
	}
	return value, &parsed, nil
}

func (v *validator) version(raw json.RawMessage) (*int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		if len(raw) > 1024 || !json.Valid(raw) {
			return nil, &ReadError{Code: "malformed"}
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		token, ok := fields["nativeNumber"]
		if !ok || len(fields) != 1 {
			v.losses["version_unmapped"]++
			return nil, nil
		}
		var number string
		if json.Unmarshal(token, &number) != nil || len(number) > 256 {
			return nil, &ReadError{Code: "malformed"}
		}
		raw = []byte(number)
	}
	if len(raw) == 0 {
		v.losses["version_unmapped"]++
		return nil, nil
	}
	if len(raw) > 256 || !json.Valid(raw) {
		return nil, &ReadError{Code: "malformed"}
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		v.losses["version_unmapped"]++
		return nil, nil
	}
	if i := strings.IndexAny(string(raw), "eE"); i >= 0 {
		exponent, err := strconv.Atoi(string(raw[i+1:]))
		if err != nil || exponent < -308 || exponent > 308 {
			v.losses["version_unmapped"]++
			return nil, nil
		}
	}
	rational, ok := new(big.Rat).SetString(string(raw))
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() || rational.Sign() < 0 || rational.Num().Int64() > 9007199254740991 {
		v.losses["version_unmapped"]++
		return nil, nil
	}
	value := rational.Num().Int64()
	return &value, nil
}

func (v *validator) flag(raw json.RawMessage) (*bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		v.losses["flag_unmapped"]++
		return nil, nil
	}
	if len(raw) > 256 || !json.Valid(raw) {
		return nil, &ReadError{Code: "malformed"}
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		v.losses["flag_unmapped"]++
		return nil, nil
	}
	return &value, nil
}

func validStringScalar(raw []byte) bool {
	// JSON validity has already established escape lengths and hexadecimal syntax.
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		code, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			next, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if next < 0xdc00 || next > 0xdfff {
				return false
			}
			i += 6
		} else if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
	}
	return true
}
