package outlookmail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

// The codes a refused or lossy read carries. A GuardError turns mail off for the sync: nothing is
// applied. A Loss means the read is incomplete, so it is not trusted to say that a message is
// gone.
const (
	CodeMailLayoutUnsupported = "outlook_mail_layout_unsupported"
	CodeBlocksDamaged         = "outlook_blocks_damaged"
	CodeMailUnmapped          = "outlook_mail_unmapped"
	CodeMailLayoutPartial     = "outlook_mail_layout_partial"
	CodeMailFolderMissing     = "outlook_mail_folder_missing"

	detailNoMailObjects = "no_mail_objects"
	detailWalkCoverage  = "walk_coverage"
	minCoveragePercent  = 80
	damagedPercent      = 2
	maxRootDepth        = 16
)

// Options tunes Collect.
type Options struct {
	// MaxBody caps one inflated body file; zero means MaxBodyBytes.
	MaxBody int64
	// ReadBodies reads the body files of messages whose body is a file. A message is read
	// only when NeedBody is nil or says its detail key needs a body.
	ReadBodies bool
	NeedBody   func(detailKey uint32) bool
	// ExpectMail is set when the archive already holds mail for the account: a store that
	// now holds no header objects is then refused, so a change to the envelope itself cannot
	// read as an empty mailbox.
	ExpectMail bool
}

// Loss is one counted loss code.
type Loss struct {
	Code  string
	Count int
}

// Message is one logical message: the current header copy, its detail, folder, recipients,
// attachments and body. Copies counts the header objects (To Me, Inbox, Sent ...) that hold it.
type Message struct {
	Account string
	// Header and Detail are embedded and both have a Key field, so m.Key is ambiguous: use
	// m.Header.Key (the header object's key) or m.Detail.Key (the logical message, equal to
	// m.DetailKey). Header.Recipients is filled only by Collect.
	Header
	Detail
	Folder      Folder
	ToMe        bool
	Attachments []Attachment
	Body        Body
	Copies      int
}

// Coverage is what one folder held in this read: the oldest and newest received time and the
// number of messages with a copy in it. A folder with no message is not listed.
type Coverage struct {
	FolderKey      uint32
	Oldest, Newest time.Time
	Count          int
}

// Notes counts what the read did, numbers only.
type Notes struct {
	HeadersSeen   int // header objects with the known tag, every copy
	Messages      int
	MissingDetail int // detail keys with a header and no detail object
	MissingFolder int // messages kept without a folder: every header copy names a folder the store does not hold
	// UnknownFolderUnattributed counts those messages too: nothing in the header or the detail
	// names the account they belong to, so one of another account could be among them when that
	// account's folder object is also missing.
	UnknownFolderUnattributed int
	OtherRoot                 int // detail keys in a folder set that is not the account's
	// OrphanAttachments and OrphanRecipients count objects whose parent key is no detail object
	// at all. Children of a message that was skipped (no folder, other account, unmapped) are
	// not counted: their parent exists.
	OrphanAttachments int
	OrphanRecipients  int
	BadString         int // string fields that were present but unreadable, left blank (not a loss)
	Unmapped          int // objects that failed to map
	ResyncedKept      int // objects reached after unknown bytes that mapped cleanly and were kept
	ResyncedSkipped   int // objects reached after unknown bytes that did not map, skipped
	OtherTagSkipped   int // attachment, folder and recipient objects with an unknown tag, skipped
	BodiesInline      int
	BodiesFile        int // file bodies read and inflated
	BodiesMissing     int
	BodiesUnreadable  int
	BodiesNone        int
	BodiesNotRead     int // file bodies left unread (state not_read)
}

// Result is what Collect read.
type Result struct {
	// RootKey is the account root Collect chose: the key its folders hang from.
	RootKey  uint32
	Messages []Message // sorted by detail key
	Folders  []Folder  // the account's folders, sorted by key
	Coverage []Coverage
	// SeenDetailKeys is every detail key a seen header names, whether or not the message mapped
	// (a header with no detail object or no folder is seen all the same), sorted. The archive
	// never records such a key as absent: the store still holds it.
	SeenDetailKeys []uint32
	// Doubtful says a header object reached after unknown bytes did not map, so the read cannot
	// tell which message it named. It makes the read untrusted for marking messages gone or
	// evicted, and for nothing else (it is no loss).
	Doubtful bool
	Notes    Notes
	Losses   []Loss
	Stats    hxstore.Stats
}

// version orders the copies of one object key: the highest change stamp, then the highest block
// offset, then the later position in the block.
type version struct {
	stamp uint64
	block int64
	pos   int
}

func (a version) newer(b version) bool {
	if a.stamp != b.stamp {
		return a.stamp > b.stamp
	}
	if a.block != b.block {
		return a.block > b.block
	}
	return a.pos > b.pos
}

// winner is the current copy of one object key, already mapped: the walk keeps what the mapper
// returned and lets the object's bytes go, because a store holds hundreds of thousands of objects
// and the read must not hold them all. err is the mapper's refusal of the winning copy; it is
// counted when the read is assembled, and an older copy that would have mapped does not replace it.
type winner[T any] struct {
	v   version
	val T
	err error
	// resynced says the copy was reached after unknown bytes: it stands in only while no clean
	// copy of the key exists.
	resynced bool
}

// classBase is what the guard and the walk need of every class, whatever it maps to.
type classBase struct {
	id, tag uint16
	refuse  bool // a tag other than the known one refuses the read
	badTags map[uint16]int
}

// seer is a class the walk offers each object to.
type seer interface {
	base() *classBase
	// see takes o if it is of the class and reports whether it was.
	see(o hxstore.Object, res *Result) bool
}

// class collects the current copy of every object key of one class, mapped to T.
type class[T any] struct {
	classBase
	wins map[uint32]winner[T]
	// mapf maps an object of the class (a resynced object is kept only when it maps cleanly).
	mapf func(hxstore.Object) (T, error)
}

func newClass[T any](id, tag uint16, refuse bool, mapf func(hxstore.Object) (T, error)) *class[T] {
	return &class[T]{classBase: classBase{id: id, tag: tag, refuse: refuse, badTags: map[uint16]int{}}, wins: map[uint32]winner[T]{}, mapf: mapf}
}

func (c *class[T]) base() *classBase { return &c.classBase }

// see offers o to the class.
func (c *class[T]) see(o hxstore.Object, res *Result) bool {
	if o.Class != c.id {
		return false
	}
	switch {
	case o.Tag != c.tag:
		c.badTags[o.Tag]++
	case o.Resynced:
		// An object reached after unknown bytes is a real object: when it maps cleanly it
		// competes under the version rule like any other copy. Only one that does not map
		// is skipped, and a header that does not map leaves the read doubtful.
		if val, err := c.mapf(o); err == nil {
			res.Notes.ResyncedKept++
			if c.takes(o) {
				c.store(o, val, nil)
			}
			break
		}
		res.Notes.ResyncedSkipped++
		if o.Class == ClassHeader {
			res.Doubtful = true
		}
	default:
		c.keep(o)
	}
	if o.Class == ClassHeader && o.Tag == TagHeader {
		res.Notes.HeadersSeen++
	}
	return true
}

// keep records o if it is the first copy of its key or beats the copy held. A copy that loses is
// not mapped.
func (c *class[T]) keep(o hxstore.Object) {
	if !c.takes(o) {
		return
	}
	val, err := c.mapf(o)
	c.store(o, val, err)
}

// takes says whether o would take its key.
func (c *class[T]) takes(o hxstore.Object) bool {
	key, v := keyOf(o)
	old, held := c.wins[key]
	switch {
	case !held:
		return true
	case old.resynced != o.Resynced:
		return !o.Resynced // a resynced copy never replaces a clean one, whatever its stamp
	}
	return v.newer(old.v)
}

// store records the mapped copy of o, which takes its key.
func (c *class[T]) store(o hxstore.Object, val T, err error) {
	key, v := keyOf(o)
	c.wins[key] = winner[T]{v: v, val: val, err: err, resynced: o.Resynced}
}

// keyOf is the object key and the version of one copy.
func keyOf(o hxstore.Object) (uint32, version) {
	key, _ := o.U32(offKey) // every known tag is longer than the key word
	stamp, _ := o.U64(offStamp)
	return key, version{stamp: stamp, block: o.BlockOffset, pos: o.PayloadPos}
}

// keys returns the object keys in ascending order.
func (c *class[T]) keys() []uint32 {
	out := make([]uint32, 0, len(c.wins))
	for k := range c.wins {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// mappedRecipient is a recipient with the detail key of the message it belongs to.
type mappedRecipient struct {
	parent uint32
	r      Recipient
}

// Collect reads every message in the store. root is the Outlook profile directory, which the
// body files are read under; account names the account the messages are stored for. It keeps the
// current copy of each header, detail, body, attachment, folder and recipient object by key,
// picks the account's folders, classifies the To Me folder, and joins everything by the detail
// key. It refuses (a *hxstore.GuardError, and no messages) when the store's layout has moved: a
// header, detail or body object with an unknown tag, no header objects in a store that had some
// (Options.ExpectMail), or an object walk that covers under 80% of the payload bytes. A torn or
// invalid block is a counted loss, not a refusal.
func Collect(ctx context.Context, s *hxstore.Store, root, account string, opt Options) (Result, error) {
	var res Result
	headers := newClass(ClassHeader, TagHeader, true, func(o hxstore.Object) (Header, error) { return mapHeader(o) })
	details := newClass(ClassDetail, TagDetail, true, func(o hxstore.Object) (Detail, error) { return mapDetail(o) })
	bodies := newClass(ClassBody, TagBody, true, func(o hxstore.Object) (Body, error) { return MapBody(o), nil }) // MapBody cannot fail
	attachments := newClass(ClassAttachment, TagAttachment, false, func(o hxstore.Object) (Attachment, error) { return mapAttachment(o) })
	folders := newClass(ClassFolder, TagFolder, false, func(o hxstore.Object) (Folder, error) { return mapFolder(o) })
	recipients := newClass(ClassRecipient, TagRecipient, false, func(o hxstore.Object) (mappedRecipient, error) {
		parent, r, err := mapRecipient(o)
		return mappedRecipient{parent, r}, err
	})
	classes := []seer{headers, details, bodies, attachments, folders, recipients}
	stats, err := s.Walk(ctx, hxstore.WalkOptions{}, func(o hxstore.Object) error {
		for _, c := range classes {
			if c.see(o, &res) {
				break
			}
		}
		return nil
	})
	res.Stats = stats
	if err != nil {
		return res, err
	}
	if g := guard(stats, classes, res.Notes.HeadersSeen, opt); g != nil {
		return res, g
	}
	for _, c := range []*classBase{&attachments.classBase, &folders.classBase, &recipients.classBase} {
		for _, n := range c.badTags {
			res.Notes.OtherTagSkipped += n
		}
	}
	afterWalk()
	if err := assemble(ctx, &res, root, account, opt, headers, details, bodies, attachments, folders, recipients); err != nil {
		return res, err
	}
	if rej := stats.BlocksRejected(); rej*100 > stats.BlocksFound*damagedPercent {
		res.Losses = append(res.Losses, Loss{CodeBlocksDamaged, rej})
	}
	n := res.Notes
	for _, l := range []Loss{
		{CodeMailUnmapped, n.Unmapped}, {CodeMailLayoutPartial, n.OtherTagSkipped}, {CodeMailFolderMissing, missingFolderLoss(n)},
	} {
		if l.Count > 0 {
			res.Losses = append(res.Losses, l)
		}
	}
	return res, nil
}

// missingFolderPercent is the share of messages without a readable folder above which the read
// is incomplete: a few are folders Outlook has not written yet, many are a layout the reader misses.
const missingFolderPercent = 5

// missingFolderLoss is the loss count for messages kept without a folder: all of them, once they
// are more than missingFolderPercent of the messages.
func missingFolderLoss(n Notes) int {
	if n.MissingFolder*100 > n.Messages*missingFolderPercent {
		return n.MissingFolder
	}
	return 0
}

// The mappers Collect calls, as variables so a test can make one fail: a walked object is never
// shorter than its tag, so the only error a mapper has cannot happen on a real walk.
var (
	mapFolder     = MapFolder
	mapHeader     = MapHeader
	mapRecipient  = MapRecipient
	mapAttachment = MapAttachment
	mapDetail     = MapDetail
)

// afterWalk is a seam for a test that cancels the context between the walk and the mapping.
var afterWalk = func() {}

// guard decides whether the read must be refused.
func guard(st hxstore.Stats, classes []seer, headers int, opt Options) error {
	var parts []string
	for _, sc := range classes {
		c := sc.base()
		if !c.refuse || len(c.badTags) == 0 {
			continue
		}
		tags := make([]int, 0, len(c.badTags))
		for t := range c.badTags {
			tags = append(tags, int(t))
		}
		sort.Ints(tags)
		for _, t := range tags {
			parts = append(parts, fmt.Sprintf("class 0x%x tag 0x%x x%d, known 0x%x", c.id, t, c.badTags[uint16(t)], c.tag)) //nolint:gosec // the tags came from uint16 keys
		}
	}
	if len(parts) > 0 {
		return &hxstore.GuardError{Code: CodeMailLayoutUnsupported, Detail: strings.Join(parts, "; ")}
	}
	if opt.ExpectMail && headers == 0 {
		return &hxstore.GuardError{Code: CodeMailLayoutUnsupported, Detail: detailNoMailObjects}
	}
	if st.PayloadBytes > 0 && (st.PayloadBytes-st.UnwalkedBytes)*100 < st.PayloadBytes*minCoveragePercent {
		return &hxstore.GuardError{Code: CodeMailLayoutUnsupported, Detail: detailWalkCoverage}
	}
	return nil
}

// assemble maps the winners and joins them.
func assemble(ctx context.Context, res *Result, root, account string, opt Options, headers *class[Header], details *class[Detail], bodies *class[Body], attachments *class[Attachment], folders *class[Folder], recipients *class[mappedRecipient]) error {
	n := &res.Notes
	// Folders.
	byKey := map[uint32]Folder{}
	for _, k := range folders.keys() {
		f, err := folders.wins[k].val, folders.wins[k].err
		if err != nil {
			n.Unmapped++
			continue
		}
		n.BadString += f.BadStrings
		byKey[f.Key] = f
	}
	// Headers, each tagged with its folder.
	var all []Header
	for _, k := range headers.keys() {
		h, err := headers.wins[k].val, headers.wins[k].err
		if err != nil {
			n.Unmapped++
			continue
		}
		n.BadString += h.BadStrings
		h.Offset = headers.wins[k].v.block
		all = append(all, h)
	}
	seenKeys := map[uint32]bool{}
	for _, h := range all {
		if !seenKeys[h.DetailKey] {
			seenKeys[h.DetailKey] = true
			res.SeenDetailKeys = append(res.SeenDetailKeys, h.DetailKey)
		}
	}
	sort.Slice(res.SeenDetailKeys, func(i, j int) bool { return res.SeenDetailKeys[i] < res.SeenDetailKeys[j] })
	rootKey := accountRoot(byKey, all)
	res.RootKey = rootKey
	inRoot := map[uint32]Folder{}
	for k, f := range byKey {
		if rootOf(byKey, k) == rootKey {
			inRoot[k] = f
		}
	}
	classifyToMe(inRoot, all)
	for _, k := range sortedFolderKeys(inRoot) {
		res.Folders = append(res.Folders, inRoot[k])
	}
	// Join the copies by detail key. A copy in a folder the store does not hold (every copy of the
	// folder object may be unreadable) is kept apart: the message is shown from it, in a folder of
	// kind unknown, only when it has no copy in a known folder.
	copies := map[uint32][]Header{}
	orphans := map[uint32][]Header{}
	seen := map[uint32]bool{} // detail keys counted in the notes, so each counts once
	for _, h := range all {
		_, known := byKey[h.FolderKey]
		_, mine := inRoot[h.FolderKey]
		switch {
		case !known:
			orphans[h.DetailKey] = append(orphans[h.DetailKey], h)
		case !mine:
			if !seen[h.DetailKey] {
				seen[h.DetailKey] = true
				n.OtherRoot++
			}
		default:
			copies[h.DetailKey] = append(copies[h.DetailKey], h)
		}
	}
	unknown := map[uint32]bool{}
	for dk, hs := range orphans {
		if _, has := copies[dk]; has || seen[dk] {
			continue
		}
		copies[dk] = hs
		unknown[dk] = true
	}
	// Recipients and attachments, by the message they belong to.
	recByMsg := map[uint32][]Recipient{}
	for _, k := range recipients.keys() {
		parent, r, err := recipients.wins[k].val.parent, recipients.wins[k].val.r, recipients.wins[k].err
		if err != nil {
			n.Unmapped++
			continue
		}
		n.BadString += r.BadStrings
		recByMsg[parent] = append(recByMsg[parent], r)
	}
	attByMsg := map[uint32][]Attachment{}
	for _, k := range attachments.keys() {
		a, err := attachments.wins[k].val, attachments.wins[k].err
		if err != nil {
			n.Unmapped++
			continue
		}
		n.BadString += a.BadStrings
		attByMsg[a.MessageKey] = append(attByMsg[a.MessageKey], a)
	}
	detailKeys := make([]uint32, 0, len(copies))
	for k := range copies {
		detailKeys = append(detailKeys, k)
	}
	sort.Slice(detailKeys, func(i, j int) bool { return detailKeys[i] < detailKeys[j] })
	for _, dk := range detailKeys {
		dw, ok := details.wins[dk]
		if !ok {
			n.MissingDetail++
			continue
		}
		d, err := dw.val, dw.err
		if err != nil {
			n.Unmapped++
			continue
		}
		n.BadString += d.BadStrings
		cur, toMe := currentCopy(copies[dk], inRoot)
		folder := inRoot[cur.FolderKey]
		if unknown[dk] {
			folder = Folder{Key: cur.FolderKey, Kind: KindUnknown}
			n.MissingFolder++
			n.UnknownFolderUnattributed++
		}
		m := Message{
			Account: account, Header: cur, Detail: d, Folder: folder, ToMe: toMe,
			Copies: len(copies[dk]), Attachments: attByMsg[dk],
		}
		m.Recipients = recByMsg[dk]
		if bw, ok := bodies.wins[dk]; ok {
			m.Body = bw.val
		} else {
			m.Body = Body{State: BodyNone}
		}
		delete(attByMsg, dk)
		delete(recByMsg, dk)
		res.Messages = append(res.Messages, m)
	}
	n.Messages = len(res.Messages)
	for k, a := range attByMsg {
		if _, parent := details.wins[k]; !parent {
			n.OrphanAttachments += len(a)
		}
	}
	for k, r := range recByMsg {
		if _, parent := details.wins[k]; !parent {
			n.OrphanRecipients += len(r)
		}
	}
	res.Coverage = coverage(res.Messages, copies)
	return readBodies(ctx, res, root, opt)
}

// currentCopy picks the header the message is shown from: the highest-stamp copy among those in
// a folder that is not To Me, else the highest-stamp To Me copy. toMe is true when any copy is in
// a To Me folder.
func currentCopy(copies []Header, folders map[uint32]Folder) (cur Header, toMe bool) {
	var best, bestToMe *Header
	for i := range copies {
		h := &copies[i]
		if folders[h.FolderKey].Kind == "to_me" {
			toMe = true
			if bestToMe == nil || h.newerThan(*bestToMe) {
				bestToMe = h
			}
		} else if best == nil || h.newerThan(*best) {
			best = h
		}
	}
	if best == nil {
		best = bestToMe
	}
	return *best, toMe
}

// newerThan orders header copies by stamp, then block offset.
func (h Header) newerThan(o Header) bool {
	if h.Stamp != o.Stamp {
		return h.Stamp > o.Stamp
	}
	return h.Offset > o.Offset
}

// rootOf follows parent links through folders to the key that is not a folder: the account root.
// A cycle or a chain longer than maxRootDepth ends where it stands.
func rootOf(folders map[uint32]Folder, key uint32) uint32 {
	cur := key
	for i := 0; i < maxRootDepth; i++ {
		p := folders[cur].Parent
		if _, isFolder := folders[p]; !isFolder {
			return p
		}
		cur = p
	}
	return cur
}

// accountRoot picks the folder set the mail lives in: the root whose folders hold the most header
// copies, then the one with the most folders, then the lowest key.
func accountRoot(folders map[uint32]Folder, headers []Header) uint32 {
	type score struct{ headers, folders int }
	scores := map[uint32]*score{}
	for k := range folders {
		r := rootOf(folders, k)
		if scores[r] == nil {
			scores[r] = &score{}
		}
		scores[r].folders++
	}
	for _, h := range headers {
		if _, ok := folders[h.FolderKey]; ok {
			scores[rootOf(folders, h.FolderKey)].headers++
		}
	}
	var best uint32
	var bs *score
	for r, sc := range scores {
		switch {
		case bs == nil, sc.headers > bs.headers, sc.headers == bs.headers && sc.folders > bs.folders,
			sc.headers == bs.headers && sc.folders == bs.folders && r < best:
			best, bs = r, sc
		}
	}
	return best
}

// classifyToMe settles the folders of the shared type 0x7a: one that holds a header copy whose
// detail key also has a copy in an inbox folder is To Me, every other one is Junk.
func classifyToMe(folders map[uint32]Folder, headers []Header) {
	inInbox := map[uint32]bool{}
	for _, h := range headers {
		if folders[h.FolderKey].Kind == "inbox" {
			inInbox[h.DetailKey] = true
		}
	}
	shares := map[uint32]bool{}
	for _, h := range headers {
		if folders[h.FolderKey].Kind == KindJunkOrToMe && inInbox[h.DetailKey] {
			shares[h.FolderKey] = true
		}
	}
	for k, f := range folders {
		if f.Kind != KindJunkOrToMe {
			continue
		}
		f.Kind = "junk"
		if shares[k] {
			f.Kind = "to_me"
		}
		folders[k] = f
	}
}

func sortedFolderKeys(m map[uint32]Folder) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// coverage reports, per folder, the oldest and newest received time and the count of messages
// that have a copy in it. Every copy counts toward its own folder, so the To Me folder shows its
// own reach even for messages shown from the Inbox. Unset times are left out of the range.
func coverage(msgs []Message, copies map[uint32][]Header) []Coverage {
	by := map[uint32]*Coverage{}
	for _, m := range msgs {
		inFolder := map[uint32]bool{}
		for _, h := range copies[m.DetailKey] {
			if inFolder[h.FolderKey] {
				continue
			}
			inFolder[h.FolderKey] = true
			c := by[h.FolderKey]
			if c == nil {
				c = &Coverage{FolderKey: h.FolderKey}
				by[h.FolderKey] = c
			}
			c.Count++
			if r := m.Received; !r.IsZero() {
				if c.Oldest.IsZero() || r.Before(c.Oldest) {
					c.Oldest = r
				}
				if r.After(c.Newest) {
					c.Newest = r
				}
			}
		}
	}
	keys := make([]uint32, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]Coverage, 0, len(keys))
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out
}

// readBodies counts the bodies and, when asked, reads the files of the messages that need one.
func readBodies(ctx context.Context, res *Result, root string, opt Options) error {
	limit := opt.MaxBody
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	n := &res.Notes
	for i := range res.Messages {
		m := &res.Messages[i]
		switch m.Body.State {
		case BodyInline:
			n.BodiesInline++
		case BodyUnreadable:
			n.BodiesUnreadable++
		case BodyNone:
			n.BodiesNone++
		case BodyFile:
			if !opt.ReadBodies || opt.NeedBody != nil && !opt.NeedBody(m.DetailKey) {
				m.Body.State = BodyNotRead
				n.BodiesNotRead++
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := ReadDat(root, m.Body.Path, limit)
			switch {
			case err == nil && utf8.Valid(data):
				m.Body.HTML = data
				n.BodiesFile++
			case errors.Is(err, ErrBodyMissing):
				m.Body.State = BodyMissing
				n.BodiesMissing++
			default:
				m.Body.State = BodyUnreadable
				n.BodiesUnreadable++
			}
		}
	}
	return nil
}
