package loopcache

type Channel struct {
	Path              string
	Sequence          int64
	Text              string
	Markers           int
	PendingOperations int
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	FileID                          string
	Sequence, LatestSequence        int64
	PendingOperations, MissingBlobs int
	Partial                         bool
	Channels                        []Channel
	Losses                          []Loss
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return e.Code }
