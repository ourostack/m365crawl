// Package hxbuild writes synthetic HxStore files for tests. It exists so the
// reader in internal/hxstore can be tested on bytes this repository made up:
// nothing here reads, copies or resembles a real store's content.
//
// The builder is deliberately independent of the reader. It does not import
// internal/hxstore and repeats the few format constants it needs, so a wrong
// constant in one is not silently agreed with by the other. Block payloads are
// written as literal-only LZ4 blocks (always valid, never compressed).
package hxbuild

import (
	"encoding/binary"
	"hash/crc32"
	"unicode/utf16"
)

const (
	// FileHeaderSize is the size of the file header the builder writes.
	FileHeaderSize = 0x40
	// BlockHeaderSize is the size of a block header.
	BlockHeaderSize = 0x28
	// EnvelopeSize is the size of an object envelope: u16 5, u16 tag, u32 length,
	// u16 0, u16 class.
	EnvelopeSize = 12
	// DefaultVersion and DefaultPageSize are the values a known store carries.
	DefaultVersion  = 'i'
	DefaultPageSize = 4096
	// BlockTypeData is the block type the reader parses.
	BlockTypeData = 8
)

var (
	fileMagic  = []byte("Nostromo")
	blockMagic = []byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}
)

// Options chooses the file header. A zero Version or PageSize means the default.
type Options struct {
	Version  byte
	PageSize uint64
}

// Builder accumulates a store file in memory.
type Builder struct {
	buf      []byte
	pageSize int
}

// New starts a file with a valid header.
func New(o Options) *Builder {
	if o.Version == 0 {
		o.Version = DefaultVersion
	}
	if o.PageSize == 0 {
		o.PageSize = DefaultPageSize
	}
	hdr := make([]byte, FileHeaderSize)
	copy(hdr, fileMagic)
	hdr[8] = o.Version
	binary.LittleEndian.PutUint64(hdr[0x38:], o.PageSize)
	if o.PageSize > 1<<30 {
		panic("hxbuild: page size too large")
	}
	return &Builder{buf: hdr, pageSize: int(o.PageSize)}
}

// Block appends a type-8 block holding the inflated bytes, at the current end
// of the file. Blocks are not aligned; use AlignPage for that.
func (b *Builder) Block(inflated []byte) { b.BlockType(BlockTypeData, inflated) }

// BlockType appends a block of the given type.
func (b *Builder) BlockType(typ uint32, inflated []byte) {
	b.buf = append(b.buf, EncodeBlock(typ, inflated)...)
}

// Raw appends bytes as they are: a hole, a torn block, a false magic.
func (b *Builder) Raw(p []byte) { b.buf = append(b.buf, p...) }

// Pad appends n zero bytes.
func (b *Builder) Pad(n int) { b.buf = append(b.buf, make([]byte, n)...) }

// AlignPage pads with zeros to the next multiple of the page size.
func (b *Builder) AlignPage() {
	if rem := len(b.buf) % b.pageSize; rem != 0 {
		b.Pad(b.pageSize - rem)
	}
}

// Len is the current file length.
func (b *Builder) Len() int { return len(b.buf) }

// Bytes returns a copy of the file so far.
func (b *Builder) Bytes() []byte { return append([]byte(nil), b.buf...) }

// EncodeBlock returns one complete block: header, checksums, and a literal-only
// LZ4 payload. The two CRC-32 ranges are written out here as arithmetic on
// purpose, independently of the reader: the header checksum covers bytes 4 to
// 0x20 and the payload checksum bytes 8 to 0x28 plus the payload length.
func EncodeBlock(typ uint32, inflated []byte) []byte {
	payload := LiteralLZ4(inflated)
	blk := make([]byte, BlockHeaderSize, BlockHeaderSize+len(payload))
	copy(blk[8:], blockMagic)
	binary.LittleEndian.PutUint32(blk[0x10:], typ)
	binary.LittleEndian.PutUint32(blk[0x14:], clampU32(len(payload)))
	binary.LittleEndian.PutUint32(blk[0x18:], clampU32(len(inflated)))
	binary.LittleEndian.PutUint32(blk[0x1c:], 4)
	blk = append(blk, payload...)
	binary.LittleEndian.PutUint32(blk[4:], crc32.ChecksumIEEE(blk[8:]))
	binary.LittleEndian.PutUint32(blk[0:], crc32.ChecksumIEEE(blk[4:0x20]))
	return blk
}

func clampU32(n int) uint32 {
	if n < 0 || int64(n) > int64(^uint32(0)) {
		panic("hxbuild: length does not fit in 32 bits")
	}
	return uint32(n)
}

// LiteralLZ4 encodes src as a single raw LZ4 block that carries only literals.
// It is valid for any input, including empty input.
func LiteralLZ4(src []byte) []byte {
	n := len(src)
	out := make([]byte, 0, n+n/255+2)
	if n < 15 {
		out = append(out, byte(n<<4))
	} else {
		out = append(out, 0xf0)
		for rest := n - 15; ; rest -= 255 {
			if rest < 255 {
				out = append(out, byte(rest&0xff))
				break
			}
			out = append(out, 255)
		}
	}
	return append(out, src...)
}

// Object builds one serialized object. Offsets are counted from the first byte
// of the envelope, which is where the reader's Object.Raw starts. The outer
// length word that precedes the envelope in a payload is added by Encode.
type Object struct {
	b []byte
}

// NewObject starts an object of the given class and tag whose fixed region is
// size bytes of zeros (envelope included). size is normally equal to tag; a test
// that wants a lying tag passes a different one. A size below the envelope is
// raised to the envelope size.
func NewObject(class, tag uint16, size int) *Object {
	if size < EnvelopeSize {
		size = EnvelopeSize
	}
	o := &Object{b: make([]byte, size)}
	binary.LittleEndian.PutUint16(o.b[0:], 5)
	binary.LittleEndian.PutUint16(o.b[2:], tag)
	binary.LittleEndian.PutUint16(o.b[10:], class)
	o.sync()
	return o
}

// sync writes the current length into the envelope.
func (o *Object) sync() { binary.LittleEndian.PutUint32(o.b[4:], clampU32(len(o.b))) }

// Len is the object's length in bytes, envelope included.
func (o *Object) Len() int { return len(o.b) }

// PutU8 writes one byte at off. It panics if off is outside the object, which is
// a mistake in the test, not in the data.
func (o *Object) PutU8(off int, v uint8) { o.b[off] = v }

// PutU16 writes a little-endian 16-bit value at off.
func (o *Object) PutU16(off int, v uint16) { binary.LittleEndian.PutUint16(o.b[off:], v) }

// PutU32 writes a little-endian 32-bit value at off.
func (o *Object) PutU32(off int, v uint32) { binary.LittleEndian.PutUint32(o.b[off:], v) }

// PutU64 writes a little-endian 64-bit value at off.
func (o *Object) PutU64(off int, v uint64) { binary.LittleEndian.PutUint64(o.b[off:], v) }

// Append adds raw bytes after the current end (a variable area) and returns the
// offset where they start.
func (o *Object) Append(p []byte) int {
	at := len(o.b)
	o.b = append(o.b, p...)
	o.sync()
	return at
}

// AppendString adds s as NUL-terminated UTF-16LE text after the current end and
// returns the offset where it starts.
func (o *Object) AppendString(s string) int { return o.Append(UTF16Z(s)) }

// PutStringWord appends s as NUL-terminated UTF-16LE text and writes at wordOff
// a 4-byte word holding the string's offset relative to base, the way the
// reader's Object.String finds it.
func (o *Object) PutStringWord(wordOff, base int, s string) {
	at := o.AppendString(s)
	o.PutU32(wordOff, clampU32(at-base))
}

// Encode returns the object as it sits in a payload: a u32 length followed by
// the envelope and the rest of the object, where the length equals the size of
// the object counted from the envelope.
func (o *Object) Encode() []byte {
	out := make([]byte, 4, 4+len(o.b))
	binary.LittleEndian.PutUint32(out, clampU32(len(o.b)))
	return append(out, o.b...)
}

// Payload concatenates encoded objects into an inflated block payload.
func Payload(objs ...*Object) []byte {
	var out []byte
	for _, o := range objs {
		out = append(out, o.Encode()...)
	}
	return out
}

// UTF16Z encodes s as UTF-16LE followed by a two-byte NUL terminator.
func UTF16Z(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2*len(units)+2)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return binary.LittleEndian.AppendUint16(out, 0)
}
