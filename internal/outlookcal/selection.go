package outlookcal

import (
	"errors"
	"strings"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

type SelectionReceipt struct {
	SourceID                           string
	LastModifiedRaw, StampRaw          uint64
	BlockOffset                        int64
	PayloadPos                         int
	DetailKey                          uint32
	DetailCopies, DetailResyncedCopies int
	DetailPresent, DetailDiffering     bool
}

type AttendeeSelections struct {
	Rows []SelectionReceipt
	Err  error
}

type selectionLimits struct {
	receipts, keys, selected, scalar, idBytes int
}

func defaultSelectionLimits() selectionLimits {
	return selectionLimits{receipts: 65536, keys: 65536, selected: 524288, scalar: 262144, idBytes: 8388608}
}

type detailCounts struct{ total, resynced int }

type selectionState struct {
	result            *AttendeeSelections
	cap               selectionLimits
	details           map[uint32]detailCounts
	selected, idBytes int
}

func (s *selectionState) refuse() {
	s.result.Rows = nil
	s.result.Err = errors.New("outlook_attendee_selection_too_large")
	s.details = nil
	s.selected, s.idBytes = 0, 0
}

func (s *selectionState) observe(o hxstore.Object) {
	if s.result.Err != nil || (o.Class != classEvent && o.Class != classDetail && o.Class != 0x71) {
		return
	}
	s.selected++
	if s.selected > s.cap.selected {
		s.refuse()
		return
	}
	if o.Class != classDetail || o.Tag != tagDetail {
		return
	}
	key, ok := DetailKey(o)
	if !ok || key == 0 {
		return
	}
	count, exists := s.details[key]
	if !exists && len(s.details) >= s.cap.keys {
		s.refuse()
		return
	}
	count.total++
	if o.Resynced {
		count.resynced++
	}
	s.details[key] = count
}

func (s *selectionState) emit(id string, o hxstore.Object, key uint32, present, differing bool) {
	if s.result.Err != nil {
		return
	}
	if len(s.result.Rows) >= s.cap.receipts || len(id) > s.cap.scalar || len(id) > s.cap.idBytes-s.idBytes {
		s.refuse()
		return
	}
	s.idBytes += len(id)
	modified, _ := o.U64(evLastMod)
	stamp, _ := o.U64(112)
	count := s.details[key]
	s.result.Rows = append(s.result.Rows, SelectionReceipt{
		SourceID: strings.Clone(id), LastModifiedRaw: modified, StampRaw: stamp,
		BlockOffset: o.BlockOffset, PayloadPos: o.PayloadPos, DetailKey: key,
		DetailCopies: count.total, DetailResyncedCopies: count.resynced,
		DetailPresent: present, DetailDiffering: differing,
	})
}
