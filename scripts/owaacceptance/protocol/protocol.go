// Package protocol defines the bounded private ownership and safe qualification records.
package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

const (
	Version        = 1
	TestName       = "TestRealOWAMailCountsAndRelationships"
	MaxRecordBytes = 32768
	MaxCount       = 1 << 30
	OwnerName      = "ownership.json"
	AdoptedName    = "adopted.json"
	WitnessName    = "qualification.json"
	CloseoutName   = "closeout.json"
	OwnerEnv       = "M365CRAWL_OWA_OWNER_RECORD"
)

var CountFields = []string{"messages", "folders", "body_records", "body_joined", "body_missing", "body_orphan", "body_inline", "body_none", "recipients", "to", "cc", "bcc", "read", "unread", "unknown", "attachments", "size_zero", "inline", "has_attachments", "conversation_present", "folder_ids", "conversation_groups"}

type Process struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
}

type Ownership struct {
	Version      int       `json:"version"`
	Nonce        string    `json:"nonce"`
	Test         string    `json:"test"`
	BinaryHash   string    `json:"binary_hash"`
	BinaryNative [3]uint32 `json:"binary_native"`
	Root         string    `json:"root"`
	RootNative   [3]uint32 `json:"root_native"`
	Source       string    `json:"source"`
	Supervisor   Process   `json:"supervisor"`
	Child        Process   `json:"child"`
	Adopted      bool      `json:"adopted"`
}

type Qualification struct {
	Version      int              `json:"version"`
	Nonce        string           `json:"nonce"`
	Test         string           `json:"test"`
	BinaryHash   string           `json:"binary_hash"`
	BinaryNative [3]uint32        `json:"binary_native"`
	RootNative   [3]uint32        `json:"root_native"`
	Supervisor   Process          `json:"supervisor"`
	Child        Process          `json:"child"`
	Outcome      string           `json:"outcome"`
	Observed     int64            `json:"observed"`
	Unstable     int64            `json:"unstable"`
	Counts       map[string]int64 `json:"counts"`
}

type Closeout struct {
	Version             int       `json:"version"`
	Owner               Ownership `json:"owner"`
	Outcome             string    `json:"outcome"`
	Reason              string    `json:"reason"`
	Exit                int       `json:"exit"`
	ConsumersReleased   bool      `json:"consumers_released"`
	NativeCloseVerified bool      `json:"native_close_verified"`
	RootState           string    `json:"root_state"`
	ObservedRootNative  [3]uint32 `json:"observed_root_native"`
	Qualified           bool      `json:"qualified"`
}

func refused() error { return errors.New("qualification_protocol_refused") }
func Encode(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxRecordBytes {
		return nil, refused()
	}
	return data, nil
}
func strict(data []byte, target any) error {
	if len(data) == 0 || len(data) > MaxRecordBytes {
		return refused()
	}
	if err := required(data, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return refused()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return refused()
	}
	return nil
}
func required(data []byte, typ reflect.Type) error {
	if typ.Kind() == reflect.Array {
		var elements []json.RawMessage
		if json.Unmarshal(data, &elements) != nil || len(elements) != typ.Len() {
			return refused()
		}
		for _, element := range elements {
			if bytes.Equal(bytes.TrimSpace(element), []byte("null")) {
				return refused()
			}
		}
		return nil
	}
	if typ.Kind() == reflect.Map {
		decoder := json.NewDecoder(bytes.NewReader(data))
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return refused()
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return refused()
			}
			seen[key] = true
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil || bytes.Equal(raw, []byte("null")) {
				return refused()
			}
		}
		if _, err := decoder.Token(); err != nil {
			return refused()
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return refused()
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return refused()
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(raw, []byte("null")) {
			return refused()
		}
		found := false
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Tag.Get("json") == key {
				found = true
				if required(raw, field.Type) != nil {
					return refused()
				}
			}
		}
		if !found {
			return refused()
		}
	}
	if _, err := decoder.Token(); err != nil || len(seen) != typ.NumField() {
		return refused()
	}
	return nil
}
func hash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && value == strings.ToLower(value)
}
func process(p Process) bool { return p.PID > 0 && p.Created > 0 }
func ValidateOwnership(o Ownership) error {
	if o.Version != Version || o.Test != TestName || !hash(o.Nonce) || !hash(o.BinaryHash) || o.BinaryNative == [3]uint32{} || o.RootNative == [3]uint32{} || o.Root == "" || o.Source == "" || !process(o.Supervisor) {
		return refused()
	}
	if o.Adopted {
		if !process(o.Child) || o.Child.PID == o.Supervisor.PID {
			return refused()
		}
	} else if o.Child != (Process{}) {
		return refused()
	}
	return nil
}
func DecodeOwnership(data []byte) (Ownership, error) {
	var value Ownership
	if strict(data, &value) != nil || ValidateOwnership(value) != nil {
		return Ownership{}, refused()
	}
	return value, nil
}
func DecodeQualification(data []byte) (Qualification, error) {
	var value Qualification
	if strict(data, &value) != nil || value.Version != Version || value.Test != TestName || !hash(value.Nonce) || !hash(value.BinaryHash) || value.BinaryNative == [3]uint32{} || value.RootNative == [3]uint32{} || !process(value.Supervisor) || !process(value.Child) || value.Supervisor.PID == value.Child.PID || value.Outcome != "passed" || value.Observed <= 0 || value.Observed > MaxCount || value.Unstable != 0 || len(value.Counts) != len(CountFields) {
		return Qualification{}, refused()
	}
	for _, key := range CountFields {
		count, ok := value.Counts[key]
		if !ok || count < 0 || count > MaxCount {
			return Qualification{}, refused()
		}
	}
	return value, nil
}
func DecodeCloseout(data []byte) (Closeout, error) {
	var value Closeout
	if strict(data, &value) != nil || value.Version != Version || ValidateOwnership(value.Owner) != nil || value.Exit < -1 || value.Reason == "" {
		return Closeout{}, refused()
	}
	switch value.Reason {
	case "none", "test_exit_failed", "qualification_refused", "lifecycle_refused", "cleanup_refused", "binary_close_failed":
	default:
		return Closeout{}, refused()
	}
	switch value.Outcome {
	case "passed", "test_failed", "timeout", "refused", "retained":
	default:
		return Closeout{}, refused()
	}
	switch value.RootState {
	case "absent":
		if value.ObservedRootNative != [3]uint32{} {
			return Closeout{}, refused()
		}
	case "retained_owned":
		if value.ObservedRootNative != value.Owner.RootNative {
			return Closeout{}, refused()
		}
	case "retained_unverified":
	default:
		return Closeout{}, refused()
	}
	if value.Qualified && (!value.Owner.Adopted || value.Outcome != "passed" || value.Reason != "none" || value.Exit != 0 || !value.ConsumersReleased || !value.NativeCloseVerified || value.RootState != "absent") {
		return Closeout{}, refused()
	}
	if value.Outcome == "passed" && !value.Qualified {
		return Closeout{}, refused()
	}
	return value, nil
}
func Match(owner Ownership, witness Qualification) error {
	if ValidateOwnership(owner) != nil || !owner.Adopted {
		return refused()
	}
	data, err := Encode(witness)
	if err != nil {
		return refused()
	}
	if _, err := DecodeQualification(data); err != nil {
		return refused()
	}
	if owner.Nonce != witness.Nonce || owner.Test != witness.Test || owner.BinaryHash != witness.BinaryHash || owner.BinaryNative != witness.BinaryNative || owner.RootNative != witness.RootNative || owner.Supervisor != witness.Supervisor || owner.Child != witness.Child {
		return refused()
	}
	return nil
}
