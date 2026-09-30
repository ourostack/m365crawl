package indexeddb

import (
	"encoding/binary"
	"errors"

	"github.com/golang/snappy"
	"github.com/ourostack/teamscrawl/internal/v8"
)

const (
	blinkTag           = 0xff
	blinkPseudoVersion = 0x11 // kRequiresProcessingSSVPseudoVersion
	wrapBlob           = 0x01
	wrapSnappy         = 0x02
	trailerTag         = 0xfe
	trailerSize        = 13
	trailerMinVersion  = 21
	maxEnvelopeDepth   = 4
)

// Omission codes. v8 codes (v8_unknown_tag, v8_host_object, v8_shared) pass
// through unchanged; v8_version and v8_malformed cover the other v8 errors.
const (
	CodeEmptyValue      = "empty_value"
	CodeUnknownEnvelope = "unknown_envelope"
	CodeBlobMissing     = "blob_missing"
	CodeV8Version       = "v8_version"
	CodeV8Malformed     = "v8_malformed"
)

// Omission describes a value that could not be decoded.
type Omission struct{ Code, Detail string }

// OmissionError is the typed error Decode returns; callers count it by Code.
type OmissionError struct{ Omission }

func (e *OmissionError) Error() string { return "indexeddb: " + e.Code + ": " + e.Detail }

func unknown(detail string) error {
	return &OmissionError{Omission{Code: CodeUnknownEnvelope, Detail: detail}}
}

// EnvelopeKind classifies a raw record value (Record.Raw: the stored value with
// its leading version varint already removed) as plain, snappy, blob, v21 or
// unknown. Empty input is unknown.
func EnvelopeKind(raw []byte) string {
	if len(raw) < 2 || raw[0] != blinkTag {
		return "unknown"
	}
	ver, n, err := readVarint(raw[1:])
	if err != nil {
		return "unknown"
	}
	rest := raw[1+n:]
	switch {
	case ver == blinkPseudoVersion:
		if len(rest) == 0 {
			return "unknown"
		}
		switch rest[0] {
		case wrapBlob:
			return "blob"
		case wrapSnappy:
			return "snappy"
		}
		return "unknown"
	case ver >= trailerMinVersion:
		if len(rest) > 0 && rest[0] == trailerTag {
			return "v21"
		}
		return "unknown"
	case ver >= 1:
		return "plain"
	}
	return "unknown"
}

// Decode unwraps the Blink envelope of a record value (blob replacement,
// snappy, v21 trailer) and deserializes the V8 payload. raw is Record.Raw.
// Failures are *OmissionError.
func (o *Origin) Decode(dbID int64, raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, &OmissionError{Omission{Code: CodeEmptyValue, Detail: "record has no value"}}
	}
	payload, err := o.unwrap(dbID, raw, 0)
	if err != nil {
		return nil, err
	}
	v, err := v8.Deserialize(payload)
	if err != nil {
		var ue *v8.UnsupportedError
		var ve *v8.VersionError
		switch {
		case errors.As(err, &ue):
			return nil, &OmissionError{Omission{Code: ue.Code, Detail: err.Error()}}
		case errors.As(err, &ve):
			return nil, &OmissionError{Omission{Code: CodeV8Version, Detail: err.Error()}}
		}
		return nil, &OmissionError{Omission{Code: CodeV8Malformed, Detail: err.Error()}}
	}
	return v, nil
}

// unwrap returns the V8 payload (starting at its own ff <version> header).
func (o *Origin) unwrap(dbID int64, raw []byte, depth int) ([]byte, error) {
	if depth > maxEnvelopeDepth {
		return nil, unknown("envelope nested too deeply")
	}
	if len(raw) < 2 || raw[0] != blinkTag {
		return nil, unknown("missing Blink version tag")
	}
	ver, n, err := readVarint(raw[1:])
	if err != nil {
		return nil, unknown("truncated Blink version")
	}
	pos := 1 + n
	rest := raw[pos:]
	switch {
	case ver == blinkPseudoVersion:
		if len(rest) == 0 {
			return nil, unknown("missing wrapper kind")
		}
		switch rest[0] {
		case wrapBlob:
			_, number, _, ok := parseBlobRef(raw)
			if !ok {
				return nil, unknown("malformed blob reference")
			}
			b, err := o.readBlob(dbID, number)
			if err != nil {
				return nil, err
			}
			return o.unwrap(dbID, b, depth+1)
		case wrapSnappy:
			dec, err := snappy.Decode(nil, rest[1:])
			if err != nil {
				return nil, unknown("snappy: " + err.Error())
			}
			return o.unwrap(dbID, dec, depth+1)
		}
		return nil, unknown("unrecognized wrapper kind")
	case ver >= trailerMinVersion:
		if len(rest) < trailerSize || rest[0] != trailerTag {
			return nil, unknown("missing trailer offset tag")
		}
		offset := binary.BigEndian.Uint64(rest[1:9])
		size := uint64(binary.BigEndian.Uint32(rest[9:13]))
		start := uint64(pos + trailerSize) //nolint:gosec // bounded by earlier length checks
		total := uint64(len(raw))
		if offset == 0 && size == 0 {
			return raw[start:], nil
		}
		if offset < start || offset > total || size > total-offset {
			return nil, unknown("trailer outside the value")
		}
		return raw[start:offset], nil
	case ver >= 1:
		if len(rest) > 0 && rest[0] == blinkTag {
			return rest, nil
		}
		return raw, nil
	}
	return nil, unknown("unsupported Blink version")
}
