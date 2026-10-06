package hxbuild

// The functions below make a block that a reader must refuse, each in one way.
// They copy the block they are given.

// BadHeaderCRC returns blk with the header checksum off by one bit.
func BadHeaderCRC(blk []byte) []byte {
	out := append([]byte(nil), blk...)
	out[0] ^= 1
	return out
}

// BadPayloadCRC returns blk with its last payload bit flipped, so the payload
// checksum no longer matches while the header checksum (which covers the stored
// payload checksum, not the payload) still does. blk must have a payload.
func BadPayloadCRC(blk []byte) []byte {
	out := append([]byte(nil), blk...)
	out[len(out)-1] ^= 1
	return out
}

// Truncated returns the first n bytes of blk: a block cut off by the end of the
// file.
func Truncated(blk []byte, n int) []byte { return append([]byte(nil), blk[:n]...) }

// OversizeBlock returns a block with correct checksums whose header declares an
// inflated length above what a reader may allocate. Its payload is one literal
// byte, so the lie costs nothing to store.
func OversizeBlock(declared uint32) []byte {
	return EncodeRaw(BlockTypeData, LiteralLZ4([]byte{0}), declared, HeaderConstant)
}

// BlockMagic returns a copy of the eight-byte block magic, for a test that
// plants a false one.
func BlockMagic() []byte { return append([]byte(nil), blockMagic...) }
