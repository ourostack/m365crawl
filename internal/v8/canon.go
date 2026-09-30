package v8

import "errors"

// FormatES formats f as ECMAScript Number::toString does.
func FormatES(f float64) string { return "" }

// Canonical renders v as canonical JSON.
func Canonical(v any) ([]byte, error) { return nil, errors.New("v8: not implemented") }
