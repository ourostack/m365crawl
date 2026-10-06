// Command hxstat prints counted statistics about one HxStore file as JSON. It is
// the probe used to check the reader against a private copy of a real store.
//
// Usage: hxstat PATH
//
// The output holds counts and format numbers only (blocks seen, valid and
// rejected by reason, objects per class and tag, how many of them were reached by resync, bytes no object covers, the
// header's version byte and page size). It never prints an object's bytes, a
// string, or the path. On failure it prints a coded error as JSON and exits 1.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ourostack/teamscrawl/internal/hxstore"
)

// Seams for tests: exit so a test can run main, walk so the walk failure path
// can be reached without a file that breaks mid-read.
var (
	exit = os.Exit
	walk = func(s *hxstore.Store) (hxstore.Stats, error) {
		return s.Walk(context.Background(), hxstore.WalkOptions{}, nil)
	}
)

func main() { exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// pairCount is one row of the objects-by-class-and-tag table.
type pairCount struct {
	Class uint16 `json:"class"`
	Tag   uint16 `json:"tag"`
	Count int    `json:"count"`
	// Resynced is how many of Count were reached after skipped bytes.
	Resynced int `json:"resynced"`
}

type report struct {
	VersionByte     byte           `json:"version_byte"`
	PageSize        uint64         `json:"page_size"`
	FileSize        int64          `json:"file_size"`
	BlocksFound     int            `json:"blocks_found"`
	BlocksValid     int            `json:"blocks_valid"`
	BlocksRejected  int            `json:"blocks_rejected"`
	Rejected        map[string]int `json:"rejected_by_reason"`
	PayloadBytes    int64          `json:"payload_bytes"`
	UnwalkedBytes   int64          `json:"unwalked_bytes"`
	Objects         int            `json:"objects"`
	ObjectsResynced int            `json:"objects_resynced"`
	Pairs           []pairCount    `json:"objects_by_class_tag"`
	PairsOverflow   int            `json:"pairs_overflow"`
}

type failure struct {
	Error string `json:"error"`
	// Found carries the offending header number for the two header errors.
	Found *uint64 `json:"found,omitempty"`
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: hxstat PATH")
		return 2
	}
	s, err := hxstore.OpenFile(args[0])
	if err != nil {
		return fail(stdout, err)
	}
	defer func() { _ = s.Close() }()
	st, err := walk(s)
	if err != nil {
		return fail(stdout, err)
	}
	r := report{
		VersionByte: s.Version, PageSize: s.PageSize, FileSize: s.Size(),
		BlocksFound: st.BlocksFound, BlocksValid: st.BlocksValid, BlocksRejected: st.BlocksRejected(),
		Rejected: st.Rejected, PayloadBytes: st.PayloadBytes, UnwalkedBytes: st.UnwalkedBytes,
		Objects: st.Objects, ObjectsResynced: st.ObjectsResynced, Pairs: []pairCount{}, PairsOverflow: st.PairsOverflow,
	}
	if r.Rejected == nil {
		r.Rejected = map[string]int{}
	}
	for p, n := range st.Pairs {
		r.Pairs = append(r.Pairs, pairCount{Class: p.Class, Tag: p.Tag, Count: n, Resynced: st.PairsResynced[p]})
	}
	sort.Slice(r.Pairs, func(i, j int) bool {
		if r.Pairs[i].Class != r.Pairs[j].Class {
			return r.Pairs[i].Class < r.Pairs[j].Class
		}
		return r.Pairs[i].Tag < r.Pairs[j].Tag
	})
	return emit(stdout, r, 0)
}

// fail prints a coded error. It never prints err's text, which can hold a path.
func fail(w io.Writer, err error) int {
	f := failure{Error: "read_failed"}
	var ev hxstore.ErrStoreVersion
	var ep hxstore.ErrPageSize
	switch {
	case errors.As(err, &ev):
		f.Error = "store_version"
		v := uint64(ev.Found)
		f.Found = &v
	case errors.As(err, &ep):
		f.Error = "page_size"
		f.Found = &ep.Found
	case errors.Is(err, hxstore.ErrNotHxStore):
		f.Error = "not_hxstore"
	case errors.Is(err, hxstore.ErrNotRegular):
		f.Error = "not_a_regular_file"
	case errors.Is(err, os.ErrNotExist):
		f.Error = "not_found"
	case errors.Is(err, os.ErrPermission):
		f.Error = "permission_denied"
	}
	return emit(w, f, 1)
}

func emit(w io.Writer, v any, code int) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 3
	}
	return code
}
