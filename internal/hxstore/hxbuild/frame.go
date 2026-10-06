package hxbuild

// Payload framing helpers. A payload may start with a head, and every object
// is followed by the Trailer (see FramedPayload).

// MaxTrailerHead is the longest head of the trailer form.
const MaxTrailerHead = 64

// Head returns an invented head of n bytes: 15 bytes of filler, or, for n = 15
// + 4k up to MaxTrailerHead, filler that ends in the Trailer. The filler bytes
// are arbitrary and say nothing about what a real head holds. Head panics for
// any other length, which is a mistake in the test.
func Head(n int) []byte {
	if n < 15 || n > MaxTrailerHead || (n > 15 && (n-15)%4 != 0) {
		panic("hxbuild: not a head length the framing allows")
	}
	h := make([]byte, n)
	for i := range h {
		h[i] = byte(0xa0 + i%16)
	}
	if n > 15 {
		copy(h[n-len(Trailer):], Trailer)
	}
	return h
}
