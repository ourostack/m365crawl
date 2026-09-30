package teamsdesktop

// Kinds of decoded records handed to Read's callback.
const (
	KindReplyChain   = "reply_chain"
	KindConversation = "conversation"
	KindActivity     = "activity"
)

// UnmappedError is returned by a mapper for a record it cannot map. Read counts it as the
// omission "unmapped_record" and continues.
type UnmappedError struct{ Reason string }

func (e *UnmappedError) Error() string { return "teamsdesktop: unmapped record: " + e.Reason }
