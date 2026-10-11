package loopcache

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/ourostack/m365crawl/internal/strictjson"
)

func unsupported() error { return &ReadError{Code: "loop_cache_snapshot_unsupported"} }
func tooLarge() error    { return &ReadError{Code: "loop_cache_snapshot_too_large"} }

type jsonBudget struct {
	ctx      context.Context
	maxDepth int
	maxItems int
	parser   *strictjson.Budget
}

func (b *jsonBudget) parse(raw []byte) (any, error) {
	if b.parser == nil {
		b.parser = &strictjson.Budget{Context: b.ctx, MaxDepth: b.maxDepth, MaxMembers: b.maxItems}
	}
	value, err := b.parser.Parse(raw)
	if errors.Is(err, strictjson.ErrInvalid) {
		return nil, unsupported()
	}
	if errors.Is(err, strictjson.ErrTooLarge) {
		return nil, tooLarge()
	}
	return value, err
}

func integer(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	for _, char := range number.String() {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(number.String(), 10, 64)
	return n, err == nil
}
