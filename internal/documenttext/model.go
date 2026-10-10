package documenttext

type Limits struct {
	SourceBytes, MemberBytes, SelectedBytes, TextBytes, SharedStringBytes int64
	Entries, XMLTokens, Depth, Paragraphs, Cells, Parts                   int
}

func DefaultLimits() Limits {
	return Limits{
		SourceBytes: 64 << 20, MemberBytes: 32 << 20, SelectedBytes: 64 << 20,
		TextBytes: 4 << 20, SharedStringBytes: 4 << 20,
		Entries: 8192, XMLTokens: 8388608, Depth: 128,
		Paragraphs: 65536, Cells: 131072, Parts: 512,
	}
}

type Paragraph struct {
	Part  string
	Index int
	Text  string
}

type Cell struct {
	Part, Reference, Type, Value string
	FormulaCached                bool
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Kind, State string
	Partial     bool
	Paragraphs  []Paragraph
	Cells       []Cell
	Losses      []Loss
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return e.Code }
