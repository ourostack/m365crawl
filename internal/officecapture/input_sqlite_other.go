//go:build !darwin && !windows

package officecapture

import (
	"context"
	"errors"
)

func (s *guardedInput) validateSQLite(context.Context) error {
	return errors.New("platform input qualification unavailable")
}

func registryCompanions(ctx context.Context, _ *guardedInput) (string, error) {
	return "", registryError(ctx, "office_registry_input_unqualified")
}
