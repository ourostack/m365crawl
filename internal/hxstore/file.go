package hxstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// File header layout. The magic and the version byte at +0x08 are confirmed on a
// real store; the page size at +0x38 is reference-derived and unconfirmed.
const (
	// FileHeaderSize is the size of the file header Open reads.
	FileHeaderSize = 0x40
	fileMagic      = "Nostromo"
	offVersion     = 0x08
	offPageSize    = 0x38
	// KnownPageSize is the page size of the store layout this reader was written
	// against. The block scan does not depend on pages; this is a tripwire.
	KnownPageSize = 4096

	scanWindow   = 1 << 20
	magicOverlap = len(blockMagic) - 1
	// maxPayload bounds the compressed payload of one block: the worst case LZ4
	// expansion of MaxInflated bytes plus slack. A header declaring more is not a
	// valid block, and the bound keeps the read buffer from following a hostile
	// length.
	maxPayload = MaxInflated + MaxInflated/255 + 64
)

// KnownStoreVersions lists the version bytes at +0x08 this reader accepts. Only
// 'i' was measured. The public notes also mention 'h' (a Windows sample); it is
// not accepted here because nothing in this repository was measured on it.
var KnownStoreVersions = []byte{'i'}

var (
	// ErrNotHxStore reports a file that is shorter than the header or does not
	// start with the magic "Nostromo".
	ErrNotHxStore = errors.New("hxstore: not an HxStore file")
	// ErrNotRegular reports a path that is not a regular file.
	ErrNotRegular = errors.New("hxstore: not a regular file")
)

// ErrStoreVersion reports a version byte that is not in KnownStoreVersions.
type ErrStoreVersion struct{ Found byte }

func (e ErrStoreVersion) Error() string {
	return fmt.Sprintf("hxstore: store version %#02x not known", e.Found)
}

// ErrPageSize reports a page size other than KnownPageSize.
type ErrPageSize struct{ Found uint64 }

func (e ErrPageSize) Error() string {
	return fmt.Sprintf("hxstore: page size %d not known", e.Found)
}

// Store is an open store file. It never writes to it.
type Store struct {
	r    io.ReaderAt
	size int64
	c    io.Closer
	// Version is the version byte from the file header.
	Version byte
	// PageSize is the page size from the file header.
	PageSize uint64
}

// Open checks the file header of the store in r, which is size bytes long, and
// returns a Store. It reads only the first FileHeaderSize bytes. The caller owns
// r; the Store never closes it.
func Open(r io.ReaderAt, size int64) (*Store, error) {
	if size < FileHeaderSize {
		return nil, ErrNotHxStore
	}
	var hdr [FileHeaderSize]byte
	if n, err := r.ReadAt(hdr[:], 0); n < len(hdr) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("hxstore: reading header: %w", err)
	}
	if string(hdr[:len(fileMagic)]) != fileMagic {
		return nil, ErrNotHxStore
	}
	s := &Store{
		r:        r,
		size:     size,
		Version:  hdr[offVersion],
		PageSize: binary.LittleEndian.Uint64(hdr[offPageSize:]),
	}
	if !bytes.Contains(KnownStoreVersions, []byte{s.Version}) {
		return nil, ErrStoreVersion{Found: s.Version}
	}
	if s.PageSize != KnownPageSize {
		return nil, ErrPageSize{Found: s.PageSize}
	}
	return s, nil
}

// statFile is a seam so the stat failure path can be tested.
var statFile = (*os.File).Stat

// OpenFile opens the store at path read-only and checks its header. The caller
// passes a private copy; this package never looks for Outlook's own files.
// Close the Store when done.
func OpenFile(path string) (*Store, error) {
	f, err := os.Open(path) //nolint:gosec // the caller chooses the path; read-only
	if err != nil {
		return nil, err
	}
	fi, err := statFile(f)
	if err == nil && !fi.Mode().IsRegular() {
		err = ErrNotRegular
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	s, err := Open(f, fi.Size())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	s.c = f
	return s, nil
}

// Size is the file size the Store was opened with.
func (s *Store) Size() int64 { return s.size }

// Close closes the file opened by OpenFile. It does nothing for a Store made by
// Open.
func (s *Store) Close() error {
	if s.c == nil {
		return nil
	}
	return s.c.Close()
}

// WalkOptions tunes Walk. The zero value is right for real use.
type WalkOptions struct {
	// MaxInflated is the most one block may inflate to. Zero or anything above
	// the package MaxInflated means MaxInflated.
	MaxInflated int
}

// scanner finds the block magic with a sliding window over a ReaderAt. The
// window slides forward only; a search always starts at or after the previous
// one.
type scanner struct {
	r    io.ReaderAt
	size int64
	buf  []byte
	off  int64 // file offset of buf[0]
	n    int   // valid bytes in buf
}

// next returns the file offset of the first block magic at or after from.
func (sc *scanner) next(ctx context.Context, from int64) (int64, bool, error) {
	for {
		if from+int64(len(blockMagic)) > sc.size {
			return 0, false, nil
		}
		if from < sc.off || from >= sc.off+int64(sc.n) {
			if err := sc.fill(ctx, from); err != nil {
				return 0, false, err
			}
		}
		if i := bytes.Index(sc.buf[from-sc.off:sc.n], blockMagic[:]); i >= 0 {
			return from + int64(i), true, nil
		}
		// Not in this window: continue just before its end so a magic that
		// straddles the boundary is still seen.
		end := sc.off + int64(sc.n)
		if end >= sc.size {
			return 0, false, nil
		}
		from = max(from, end-int64(magicOverlap))
		sc.n = 0 // force a refill at the new position
	}
}

func (sc *scanner) fill(ctx context.Context, at int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want := int64(len(sc.buf))
	if sc.size-at < want {
		want = sc.size - at
	}
	n, err := sc.r.ReadAt(sc.buf[:want], at)
	if int64(n) < want {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("hxstore: reading at %d: %w", at, err)
	}
	sc.off, sc.n = at, n
	return nil
}

// Walk scans the file for blocks and calls fn for every object in every valid
// block, in file order. It never holds more than one block: a 1 MiB scan window,
// one compressed block and one inflated block (at most MaxInflated each).
//
// Candidates that fail a check are counted by reason in Stats.Rejected and
// skipped; they are never an error by themselves. After a valid block the scan
// resumes at its end, after a rejected one just past its magic, so a corrupt
// block never hides its neighbours. The Object passed to fn aliases the reused
// buffer (see Object). If fn returns an error Walk stops and returns it with the
// stats so far. A nil fn only counts. Walk checks ctx once per block and once per
// scan window. The returned error is non-nil only for a read error, a cancelled
// context or an error from fn.
func (s *Store) Walk(ctx context.Context, opts WalkOptions, fn func(Object) error) (Stats, error) {
	limit := opts.MaxInflated
	if limit <= 0 || limit > MaxInflated {
		limit = MaxInflated
	}
	var st Stats
	sc := &scanner{r: s.r, size: s.size, buf: make([]byte, scanWindow)}
	var blockBuf, inflated []byte
	from := int64(0)
	for {
		magicAt, ok, err := sc.next(ctx, from)
		if err != nil {
			return st, err
		}
		if !ok {
			return st, nil
		}
		if err := ctx.Err(); err != nil {
			return st, err
		}
		st.BlocksFound++
		from = magicAt + int64(len(blockMagic))
		start := magicAt - offMagic
		var blk Block
		var out []byte
		blockBuf, blk, out, err = s.readBlock(blockBuf, inflated, start, limit)
		if err != nil {
			if reason, soft := classify(err); soft {
				st.reject(reason)
				continue
			}
			return st, err
		}
		if cap(out) > cap(inflated) {
			inflated = out[:cap(out)]
		}
		st.BlocksValid++
		st.PayloadBytes += int64(len(out))
		covered, err := walkObjects(out, func(pos int, class, tag uint16, raw []byte) error {
			st.countPair(Pair{Class: class, Tag: tag})
			if fn == nil {
				return nil
			}
			return fn(Object{BlockOffset: start, PayloadPos: pos, Class: class, Tag: tag, Raw: raw})
		})
		st.UnwalkedBytes += int64(len(out) - covered)
		if err != nil {
			return st, err
		}
		from = start + int64(blk.Len())
	}
}

// classify maps a block error to a rejection reason. soft is false for errors
// that are not about the block's content (a read failure).
func classify(err error) (reason string, soft bool) {
	switch {
	case errors.Is(err, ErrBlockTruncated):
		return RejectTruncated, true
	case errors.Is(err, ErrBlockHeaderUnknown):
		return RejectHeaderUnknown, true
	case errors.Is(err, ErrBlockTypeOther):
		return RejectTypeOther, true
	case errors.Is(err, ErrBlockOversize):
		return RejectOversize, true
	case errors.Is(err, ErrBlockHeaderCRC):
		return RejectHeaderCRC, true
	case errors.Is(err, ErrBlockPayloadCRC):
		return RejectPayloadCRC, true
	case errors.Is(err, ErrBlockInflate):
		return RejectInflate, true
	}
	return "", false
}

// errReadBlock wraps a failure to read the file, which is not a property of the
// block.
type errReadBlock struct{ err error }

func (e errReadBlock) Error() string { return "hxstore: reading block: " + e.err.Error() }
func (e errReadBlock) Unwrap() error { return e.err }

// readBlock reads and verifies the block that starts at file offset start. It
// reads the 40-byte header first and checks the lengths it declares against the
// file and against maxPayload before reading the payload, so a hostile length
// cannot make it allocate. buf and inflated are reusable buffers.
func (s *Store) readBlock(buf, inflated []byte, start int64, limit int) ([]byte, Block, []byte, error) {
	if s.size-start < HeaderSize {
		return buf, Block{}, nil, ErrBlockTruncated
	}
	buf = grow(buf, HeaderSize, 0)
	if err := s.readFull(buf[:HeaderSize], start); err != nil {
		return buf, Block{}, nil, err
	}
	total := HeaderSize
	// ParseBlock on the bare header reports the header-level problems in their
	// documented order; ErrBlockTruncated here only means "the payload is next".
	if _, err := ParseBlock(buf[:HeaderSize]); err != nil && !errors.Is(err, ErrBlockTruncated) {
		return buf, Block{}, nil, err
	}
	// ParseBlock checks the payload length before the header checksum. Check the
	// checksum first here, so a torn header is reported as that and not as a
	// truncation caused by a garbage length.
	if crc32.ChecksumIEEE(buf[headerCRCFrom:headerCRCTo]) != binary.LittleEndian.Uint32(buf[offHeaderCRC:]) {
		return buf, Block{}, nil, ErrBlockHeaderCRC
	}
	payloadLen := int64(binary.LittleEndian.Uint32(buf[offPayloadLen:]))
	switch {
	case payloadLen > maxPayload:
		return buf, Block{}, nil, ErrBlockOversize
	case start+HeaderSize+payloadLen > s.size:
		return buf, Block{}, nil, ErrBlockTruncated
	}
	total += int(payloadLen)
	buf = grow(buf, total, HeaderSize)
	if err := s.readFull(buf[HeaderSize:total], start+HeaderSize); err != nil {
		return buf, Block{}, nil, err
	}
	blk, out, err := VerifyBlock(buf[:total], inflated, limit)
	return buf, blk, out, err
}

func (s *Store) readFull(p []byte, at int64) error {
	n, err := s.r.ReadAt(p, at)
	if n < len(p) {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return errReadBlock{err}
	}
	return nil
}

// grow returns buf with length n, keeping its first keep bytes and reallocating
// only when capacity is short.
func grow(buf []byte, n, keep int) []byte {
	if cap(buf) < n {
		nb := make([]byte, n)
		copy(nb, buf[:keep])
		return nb
	}
	return buf[:n]
}
