package teamsdesktop

import (
	"crypto/sha256"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
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
//   - every rule table of Scrub and Denied: the whole of rules (see rules.go), hashed field by
//     field, so a new secret key name, regular expression, depth limit, deny term or any other
//     entry changes the signature without anybody having to remember to say so.
//
// What no table can show is a change to the code that applies the rules, to the decoder or to the
// mappers: those still need DecoderVersion raised.
func MemoSignature(derivationVersion int) []byte {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "decoder=%d derivation=%d\n", decoderVersion, derivationVersion)
	hashValue(h, "rules", reflect.ValueOf(rules))
	return h.Sum(nil)
}

// hashValue writes a canonical, unambiguous rendering of v: every value is tagged with its path,
// strings are quoted, maps and sets are written in key order. It panics on a kind it does not
// know, so a rule table of a new kind cannot be skipped silently.
func hashValue(w io.Writer, path string, v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		_, _ = fmt.Fprintf(w, "%s=%s\n", path, strconv.Quote(v.String()))
	case reflect.Int, reflect.Int64:
		_, _ = fmt.Fprintf(w, "%s=%d\n", path, v.Int())
	case reflect.Bool:
		_, _ = fmt.Fprintf(w, "%s=%t\n", path, v.Bool())
	case reflect.Pointer:
		if re, ok := v.Interface().(*regexp.Regexp); ok {
			_, _ = fmt.Fprintf(w, "%s=re:%s\n", path, strconv.Quote(re.String()))
			return
		}
		panic("teamsdesktop: rule table of unsupported kind " + v.Type().String() + " at " + path)
	case reflect.Slice:
		_, _ = fmt.Fprintf(w, "%s=[%d]\n", path, v.Len())
		for i := range v.Len() {
			hashValue(w, fmt.Sprintf("%s[%d]", path, i), v.Index(i))
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			panic("teamsdesktop: rule table with non-string keys at " + path)
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		_, _ = fmt.Fprintf(w, "%s={%d}\n", path, len(keys))
		for _, k := range keys {
			hashValue(w, path+"["+strconv.Quote(k.String())+"]", v.MapIndex(k))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			hashValue(w, path+"."+v.Type().Field(i).Name, v.Field(i))
		}
	default:
		panic("teamsdesktop: rule table of unsupported kind " + v.Type().String() + " at " + path)
	}
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
