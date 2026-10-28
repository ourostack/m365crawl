package workiq

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ourostack/m365crawl/internal/strictjson"
)

func decodeEntity(ctx context.Context, raw json.RawMessage, target any) (json.RawMessage, error) {
	if len(raw) > maxFrame {
		return nil, failure("workiq_page_too_large")
	}
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	if _, err := budget.Parse(raw); err != nil {
		return nil, failure("workiq_protocol")
	}
	safe, _ := scrubServer(raw)
	if json.Unmarshal(safe, target) != nil {
		return nil, failure("workiq_protocol")
	}
	return safe, nil
}

func graphTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, failure("workiq_protocol")
	}
	value = value.UTC()
	return &value, nil
}
