package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
	"golang.org/x/sys/windows"
)

type qualifiedRun struct {
	receipt             string
	receiptNative       [3]uint32
	owner               protocol.Ownership
	binary              *protocol.BinaryPin
	pins                []windows.Handle
	witness             protocol.Qualification
	consumersReleased   bool
	cleanupVerified     bool
	nativeCloseVerified bool
}

func (q *qualifiedRun) closePins() error {
	var result error
	for i := len(q.pins) - 1; i >= 0; i-- {
		if windows.CloseHandle(q.pins[i]) != nil {
			result = errors.Join(result, errors.New("qualification_pin_close_failed"))
		}
	}
	q.pins = nil
	return result
}

func (q *qualifiedRun) pinAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		h, _, err := lockObject(current, false)
		if err != nil {
			return errors.New("qualification_ancestor_pin_failed")
		}
		q.pins = append(q.pins, h)
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func newQualifiedRun(binary, digest, source, parent, receipt string) (*workspace, *qualifiedRun, error) {
	if protocol.SafeAbsolute(source, true, true) != nil || !filepath.IsAbs(receipt) || filepath.Clean(receipt) != receipt || protocol.SafeAbsolute(filepath.Dir(receipt), true, true) != nil {
		return nil, nil, errors.New("qualification_arguments_refused")
	}
	if protocol.StorageOutsideSource(source, parent, receipt) != nil {
		return nil, nil, errors.New("qualification_storage_placement_refused")
	}
	pin, err := protocol.PinBinary(binary, digest)
	if err != nil {
		return nil, nil, errors.New("qualification_binary_refused")
	}
	q := &qualifiedRun{receipt: receipt, binary: pin}
	fail := func(code string) (*workspace, *qualifiedRun, error) {
		closeErr := errors.Join(q.closePins(), q.binary.Close())
		return nil, nil, errors.Join(errors.New(code), closeErr)
	}
	if err := os.Mkdir(receipt, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) || protocol.CheckPrivate(receipt, true) != nil {
			return fail("qualification_receipt_directory_refused")
		}
	} else if makePrivate(receipt) != nil {
		return fail("qualification_receipt_acl_failed")
	}
	if protocol.CheckPrivate(receipt, true) != nil {
		return fail("qualification_receipt_acl_refused")
	}
	q.receiptNative, err = protocol.NativeIdentity(receipt, true)
	if err != nil {
		return fail("qualification_receipt_identity_refused")
	}
	if q.pinAncestors(receipt) != nil || q.pinAncestors(filepath.Dir(binary)) != nil {
		return fail("qualification_pin_refused")
	}
	w, err := newWorkspace(parent)
	if err != nil {
		return fail("qualification_workspace_refused")
	}
	abortWorkspace := func(code string) (*workspace, *qualifiedRun, error) {
		cleanupErr := w.cleanup()
		_, _, closeErr := fail(code)
		return nil, nil, errors.Join(cleanupErr, closeErr)
	}
	pid := os.Getpid()
	if pid <= 0 || int64(pid) > 0xffffffff {
		return abortWorkspace("qualification_supervisor_refused")
	}
	// #nosec G115 -- The positive uint32 PID range is checked immediately above.
	supervisor, err := protocol.ProcessIdentity(uint32(pid))
	if err != nil {
		return abortWorkspace("qualification_supervisor_identity_refused")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return abortWorkspace("qualification_nonce_failed")
	}
	q.owner = protocol.Ownership{Version: protocol.Version, Nonce: hex.EncodeToString(nonce[:]), Test: protocol.TestName, BinaryHash: pin.Hash, BinaryNative: pin.Native, Root: w.dir, RootNative: w.id, Source: source, Supervisor: supervisor}
	data, err := protocol.Encode(q.owner)
	if err != nil || protocol.WriteProtectedNew(filepath.Join(receipt, protocol.OwnerName), data) != nil {
		return abortWorkspace("qualification_prelaunch_record_failed")
	}
	w.qualification = q
	return w, q, nil
}

func (q *qualifiedRun) beforeResume(pid uint32) error {
	child, err := protocol.ProcessIdentity(pid)
	if err != nil {
		return errors.New("qualification_child_identity_refused")
	}
	q.owner.Child = child
	q.owner.Adopted = true
	data, err := protocol.Encode(q.owner)
	if err != nil || protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.AdoptedName), data) != nil {
		return errors.New("qualification_adoption_record_failed")
	}
	return nil
}

func (q *qualifiedRun) verifyWitness() error {
	data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.WitnessName))
	if err != nil {
		return errors.New("qualification_witness_missing")
	}
	witness, err := protocol.DecodeQualification(data)
	if err != nil || protocol.Match(q.owner, witness) != nil {
		return errors.New("qualification_witness_refused")
	}
	q.witness = witness
	return nil
}

func qualifiedEnv(env []string, q *qualifiedRun) []string {
	var out []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "M365CRAWL_") || strings.HasPrefix(name, "OWA_") || name == "GOCOVERDIR" || name == "GOFLAGS" || name == "GODEBUG" || name == "GOTRACEBACK" {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "M365CRAWL_REAL_CACHE=1", "M365CRAWL_OUTLOOK_ROOT="+q.owner.Source, "M365CRAWL_TEAMS_ROOT="+filepath.Join(q.owner.Root, "no-teams"), protocol.OwnerEnv+"="+filepath.Join(q.receipt, protocol.AdoptedName))
}

func (q *qualifiedRun) finish(exit int, result error) error {
	closeErr := errors.Join(q.binary.Close(), q.closePins())
	record := protocol.Closeout{Version: protocol.Version, Owner: q.owner, Outcome: "refused", Reason: "qualification_refused", Exit: exit, ConsumersReleased: q.consumersReleased, NativeCloseVerified: q.nativeCloseVerified && closeErr == nil && q.cleanupVerified, RootState: "retained_unverified"}
	// #nosec G703 -- Exact generated workspace root from protected ownership; read-only post-barrier absence check, never caller-directed deletion.
	if _, err := os.Lstat(q.owner.Root); errors.Is(err, os.ErrNotExist) {
		record.RootState = "absent"
	} else if id, err := protocol.NativeIdentity(q.owner.Root, true); err == nil {
		record.ObservedRootNative = id
		if id == q.owner.RootNative && protocol.CheckPrivate(q.owner.Root, true) == nil {
			record.RootState = "retained_owned"
		}
	}
	switch {
	case record.RootState != "absent":
		record.Outcome = "retained"
		record.Reason = "cleanup_refused"
	case closeErr != nil:
		record.Reason = "binary_close_failed"
	case errors.Is(result, errTimeout):
		record.Outcome = "timeout"
		record.Reason = "lifecycle_refused"
	case result != nil:
		record.Reason = "lifecycle_refused"
	case exit != 0:
		record.Outcome = "test_failed"
		record.Reason = "test_exit_failed"
	case q.witness.Outcome == "passed" && q.consumersReleased && record.NativeCloseVerified:
		record.Outcome = "passed"
		record.Reason = "none"
		record.Qualified = true
	}
	identity, identityErr := protocol.NativeIdentity(q.receipt, true)
	data, err := protocol.Encode(record)
	if err != nil || identityErr != nil || identity != q.receiptNative || protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.CloseoutName), data) != nil {
		return errors.Join(result, closeErr, errors.New("qualification_closeout_record_failed"))
	}
	if _, err := protocol.DecodeCloseout(data); err != nil {
		return errors.Join(result, closeErr, errors.New("qualification_closeout_refused"))
	}
	if !record.Qualified {
		return errors.Join(result, closeErr, errors.New("qualification_unproved"))
	}
	return nil
}

func runQualified(binary, digest, source, parent, receipt string, timeout time.Duration) (protocol.Qualification, error) {
	w, q, err := newQualifiedRun(binary, digest, source, parent, receipt)
	if err != nil {
		return protocol.Qualification{}, err
	}
	exit, result := w.run(binary, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), timeout)
	if err := q.finish(exit, result); err != nil {
		return protocol.Qualification{}, err
	}
	return q.witness, nil
}
