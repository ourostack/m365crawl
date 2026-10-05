// Package hxstore reads the block container of the new Outlook for Mac message
// store (HxStore.hxd). It is pure: it imports only the standard library and
// knows nothing about calendars, mail or the archive.
//
// The block layout (two CRC-32 checksums, a magic, a type, the compressed and
// inflated lengths, an LZ4 block payload) was learned from the public
// reverse-engineering notes at github.com/ukd1/hxstore-reverse-engineering (MIT
// licence, taken on Outlook 16.107) and checked against a copy of a real store
// taken on 16.115. No code was copied from that project.
package hxstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// Block layout constants. All integers are little-endian.
const (
	// HeaderSize is the size of a block header; the payload starts here.
	HeaderSize = 0x28
	// BlockTypeData is the only block type this reader parses.
	BlockTypeData = 8

	offHeaderCRC  = 0x00 // crc32 over bytes [4, 0x20)
	offPayloadCRC = 0x04 // crc32 over bytes [8, 0x28+payload length)
	offMagic      = 0x08
	offType       = 0x10
	offPayloadLen = 0x14
	offInflated   = 0x18
	offConstant   = 0x1c // 4 in every observed block
	headerCRCFrom = 0x04
	headerCRCTo   = 0x20
	payloadCRCAt  = 0x08
	headerConst   = 4
)

// blockMagic is the 8-byte marker at +0x08 that block finding scans for.
var blockMagic = [8]byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}

// Block rejection reasons, each a distinct sentinel. The scan in a later slice
// counts them: ErrBlockTruncated, ErrBlockMagic and ErrBlockHeaderUnknown are
// header problems, ErrBlockTypeOther and ErrBlockOversize are skipped blocks,
// and the CRC and inflate errors mean an invalid (torn or rewritten) block.
var (
	// ErrBlockTruncated reports that the bytes end before the header or the
	// payload the header declares.
	ErrBlockTruncated = errors.New("hxstore: block truncated")
	// ErrBlockMagic reports that the block magic is not at +0x08.
	ErrBlockMagic = errors.New("hxstore: block magic missing")
	// ErrBlockHeaderUnknown reports that the constant at +0x1c is not 4, which
	// means the header layout changed.
	ErrBlockHeaderUnknown = errors.New("hxstore: block header layout unknown")
	// ErrBlockTypeOther reports a block type other than 8.
	ErrBlockTypeOther = errors.New("hxstore: block type not parsed")
	// ErrBlockOversize reports an inflated length of zero or above MaxInflated
	// (or above the limit the caller allowed).
	ErrBlockOversize = errors.New("hxstore: block inflated length not allowed")
	// ErrBlockHeaderCRC reports a header checksum mismatch.
	ErrBlockHeaderCRC = errors.New("hxstore: block header checksum mismatch")
	// ErrBlockPayloadCRC reports a payload checksum mismatch.
	ErrBlockPayloadCRC = errors.New("hxstore: block payload checksum mismatch")
	// ErrBlockInflate reports a payload that does not decode to the declared
	// length. The error wraps the specific ErrLZ4 sentinel.
	ErrBlockInflate = errors.New("hxstore: block payload does not inflate")
)

// Block is a parsed block header and its payload. Payload aliases the bytes
// passed to ParseBlock.
type Block struct {
	Type        uint32
	InflatedLen int
	Payload     []byte
}

// Len is the size of the block on disk: the header plus the compressed payload.
func (b Block) Len() int { return HeaderSize + len(b.Payload) }

// ParseBlock validates the block that starts at b[0] and returns it. The checks
// run in a fixed order and stop at the first failure:
//
//  1. the header fits, the magic is at +0x08 and +0x1c is 4;
//  2. the type is 8;
//  3. the inflated length is between 1 and MaxInflated;
//  4. the payload the header declares fits in b;
//  5. the header checksum matches (IEEE CRC-32 over bytes 4 to 0x20);
//  6. the payload checksum matches (IEEE CRC-32 over bytes 8 to 0x28+length).
//
// b may be longer than the block. ParseBlock does not decompress; call Inflate
// for the last check, or use VerifyBlock for all of them.
func ParseBlock(b []byte) (Block, error) {
	if len(b) < HeaderSize {
		return Block{}, ErrBlockTruncated
	}
	if [8]byte(b[offMagic:offMagic+8]) != blockMagic {
		return Block{}, ErrBlockMagic
	}
	if binary.LittleEndian.Uint32(b[offConstant:]) != headerConst {
		return Block{}, ErrBlockHeaderUnknown
	}
	typ := binary.LittleEndian.Uint32(b[offType:])
	if typ != BlockTypeData {
		return Block{}, ErrBlockTypeOther
	}
	inflated := binary.LittleEndian.Uint32(b[offInflated:])
	if inflated < 1 || inflated > MaxInflated {
		return Block{}, ErrBlockOversize
	}
	payloadLen := uint64(binary.LittleEndian.Uint32(b[offPayloadLen:]))
	if HeaderSize+payloadLen > uint64(len(b)) {
		return Block{}, ErrBlockTruncated
	}
	end := HeaderSize + int(payloadLen)
	if crc32.ChecksumIEEE(b[headerCRCFrom:headerCRCTo]) != binary.LittleEndian.Uint32(b[offHeaderCRC:]) {
		return Block{}, ErrBlockHeaderCRC
	}
	if crc32.ChecksumIEEE(b[payloadCRCAt:end]) != binary.LittleEndian.Uint32(b[offPayloadCRC:]) {
		return Block{}, ErrBlockPayloadCRC
	}
	return Block{Type: typ, InflatedLen: int(inflated), Payload: b[HeaderSize:end]}, nil
}

// Inflate decompresses the payload into dst's storage (see decodeBlock) and
// returns exactly InflatedLen bytes. limit is the most the caller will allow for
// one block; decodeBlock caps it at MaxInflated, and it is checked before anything is
// allocated. A payload that decodes to any other length is an error.
func (b Block) Inflate(dst []byte, limit int) ([]byte, error) {
	if b.InflatedLen > limit {
		return nil, ErrBlockOversize
	}
	out, err := decodeBlock(dst, b.Payload, b.InflatedLen, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlockInflate, err)
	}
	return out, nil
}

// VerifyBlock runs every check on the block that starts at b[0]: ParseBlock, then
// Inflate. It returns the block and its inflated bytes.
func VerifyBlock(b, dst []byte, limit int) (Block, []byte, error) {
	blk, err := ParseBlock(b)
	if err != nil {
		return Block{}, nil, err
	}
	out, err := blk.Inflate(dst, limit)
	if err != nil {
		return Block{}, nil, err
	}
	return blk, out, nil
}
