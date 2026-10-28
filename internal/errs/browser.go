package errs

import "errors"

// ErrBrowserDocumentTransition discards a readonly bound evaluation that crossed
// a same-root document transition. It is not a successful navigation or result.
var ErrBrowserDocumentTransition = errors.New("browser document changed during evaluation")
