//go:build !darwin && !linux

package scoutcapture

import "context"

// Scan is refused: reading OneDrive placeholders without downloading them has not been verified
// on this platform.
func Scan(context.Context, Options, func(File) error) (Result, error) {
	return Result{}, ErrPlatformUnqualified
}

// Check is refused for the same reason as Scan.
func Check(context.Context, string) (string, error) { return "", ErrPlatformUnqualified }

// Supported is false: acquisition is refused here.
func Supported() bool { return false }

// Discover is refused for the same reason as Scan.
func Discover(context.Context, string) ([]string, error) {
	return nil, ErrPlatformUnqualified
}
