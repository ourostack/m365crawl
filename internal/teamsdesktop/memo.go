package teamsdesktop

import (
	"crypto/sha256"
	"fmt"
	"strconv"
)

// DigestLen is the length of a record digest.
const DigestLen = 16

// MemoSignature identifies how this build turns a record's bytes into archive rows. A record
// digest covers it, so a remembered digest stops matching, and the record is read again, the
// moment anything that shapes the rows changes. It covers:
//
//   - DecoderVersion, the number the code says to raise whenever decoding or mapping output
//     changes (the same number that makes the next sync re-read an unchanged cache);
//   - derivationVersion, the store's DerivationVersion, which versions the derived fields;
//   - what Scrub and Denied do, by running them over a fixed set of probes and hashing the
//     answers, so a changed scrub rule or deny list invalidates the memory even when nobody
//     remembers to raise a number.
//
// The probes can not see a change to the mappers, so a mapper change still needs DecoderVersion.
func MemoSignature(derivationVersion int) []byte {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "decoder=%d derivation=%d\n", decoderVersion, derivationVersion)
	for _, p := range scrubProbes {
		out, n := Scrub([]byte(p))
		_, _ = fmt.Fprintf(h, "scrub %d %d %s\n", len(out), n, out)
	}
	for _, n := range denyProbes {
		_, _ = fmt.Fprintf(h, "deny %t %s\n", Denied(n), strconv.Quote(n))
	}
	return h.Sum(nil)
}

// scrubProbes cover each rule of Scrub once; denyProbes cover each way Denied matches.
var scrubProbes = []string{
	`{"access_token":"abc","n":1}`,
	`{"Authorization":"Bearer xyz","keep":"me"}`,
	`"Bearer secret value"`,
	`"eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0.sig"`,
	`"https://x.test/f?a=1&SIG=deadbeef&b=2"`,
	`{"type":"password","value":"hunter2"}`,
	`{"k":"{\"refresh_token\":\"r\",\"x\":2}"}`,
	`{"$map":[["client_secret","s"],["ok","v"]]}`,
	`{"plain":"nothing to see","list":[1,2,3]}`,
}

var denyProbes = []string{
	"Teams:auth:x", "authority", "my-token-store", "credentialStore", "client_secret", "cookies",
	"msal.cache", "oneauth", "key-store", "keystore", "keyval-store", "sessions", "key-value", "keyring",
	"crypto", "encrypted", "pkce", "bearer", "password", "refresh", "adal", "aad", "aad-cache", "AADToken",
	"keys", "userKeys", "monkeys", "jwt", "e2ee", "load", "conversation-manager", "calendar-manager",
}

// recordDigest is the digest of one record: the signature, the database the record lives in (its
// name carries the account) and the record's payload, the V8 bytes that remain after the Blink
// envelope, snappy and any blob file are unwrapped, so it covers what the value is, not where it
// is stored.
func recordDigest(sig []byte, database string, payload []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(sig)
	_, _ = fmt.Fprintf(h, "%d:%s", len(database), database)
	_, _ = h.Write(payload)
	return h.Sum(nil)[:DigestLen]
}
