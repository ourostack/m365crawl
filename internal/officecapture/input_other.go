//go:build !darwin && !windows

package officecapture

import (
	"context"
	"errors"
	"os"
)

func withInputGuard(ctx context.Context, _ func() error) error {
	return jsonError(ctx, "office_json_input_unqualified")
}

func openInput(string, bool) (*guardedInput, error) {
	return nil, errors.New("platform input qualification unavailable")
}

func inputSignature(*os.File, bool) (string, error) {
	return "", errors.New("platform input qualification unavailable")
}
