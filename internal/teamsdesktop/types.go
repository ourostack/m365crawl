package teamsdesktop

import "time"

// Message is one Teams message mapped from a reply-chain record.
type Message struct {
	TenantID, UserID              string // owning account (UserID is the bare GUID, as in Account)
	ConversationID, ID            string
	ReplyChainID, ParentMessageID string
	ClientMessageID               string
	SenderID                      string // full MRI as Teams stores it, e.g. "8:orgid:<guid>"
	SenderName                    string
	SentAt                        time.Time
	EditedAt, DeletedAt           time.Time // zero when not edited / not deleted
	MessageType, ContentType      string
	ContentHTML, ContentText      string
	Version                       int64
	Mentions                      []Mention
	MentionsMe                    bool
	Reactions                     []Reaction
	Files                         []File
	Links                         []string
	Subject, Importance           string
	Pinned                        bool
	Link                          string // Teams deep link to this message
	Raw                           []byte // canonical JSON of the source message record
}

// Mention is a person or tag mentioned in a message.
type Mention struct{ ID, DisplayName string }

// Reaction is one reaction kind on a message.
type Reaction struct {
	Key     string // Teams emotion key, e.g. "like"
	Count   int
	UserIDs []string
}

// File is an attachment reference on a message (metadata only; never downloaded).
type File struct{ Name, URL, Type string }

// Conversation is a chat, channel, meeting chat or space.
type Conversation struct {
	TenantID, UserID           string
	ID                         string
	Kind                       string // Teams type, e.g. "Chat", "Topic", "Space", "Meeting"
	Title, Topic               string
	DisplayName                string // "Team › Channel" for channels, chat title otherwise
	TeamID, ParentID           string
	Members                    []string // member MRIs
	LastMessageAt              time.Time
	ReadHorizonAt              time.Time
	ReadHorizonClientMessageID string
	Favorite                   bool
	Raw                        []byte
}

// Person is someone seen as a sender or member.
type Person struct {
	TenantID, ID string // ID is the full MRI
	DisplayName  string
	SeenAt       time.Time
}

// Activity is one activity-feed item (mention, reply, reaction, ...).
type Activity struct {
	TenantID, UserID string
	ID               string
	Type, Subtype    string
	IsRead           bool
	At               time.Time
	ConversationID   string
	MessageID        string
	ReplyChainID     string
	AppID            string
	Raw              []byte
}
