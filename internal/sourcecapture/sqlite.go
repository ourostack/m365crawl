package sourcecapture

// SQLite's rollback-journal byte locks: a reader takes the pending byte, then the shared range,
// then drops the pending byte, and keeps the shared range while it reads.
const (
	pendingByte = 1073741824
	sharedFirst = 1073741826
	sharedBytes = 510
)
