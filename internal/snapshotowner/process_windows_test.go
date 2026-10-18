//go:build windows

package snapshotowner

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSnapshotOwnerWindowsProcessEvidence(t *testing.T) {
	open, query, close := ownerOpenProcess, ownerExitCode, ownerCloseHandle
	self := ownerSelfPID
	ownerSelfPID = func() int { return 4243 }
	t.Cleanup(func() {
		ownerOpenProcess, ownerExitCode, ownerCloseHandle = open, query, close
		ownerSelfPID = self
	})
	fault := errors.New("synthetic handle failure")
	for _, tc := range []struct {
		name                        string
		code                        uint32
		openErr, queryErr, closeErr error
		dead                        bool
	}{
		{name: "live-or-reused", code: 259},
		{name: "terminated", code: 0, dead: true},
		{name: "terminated-nonzero", code: 42, dead: true},
		{name: "access-denied-unknown", openErr: windows.ERROR_ACCESS_DENIED},
		{name: "invalid-parameter-unknown", openErr: windows.ERROR_INVALID_PARAMETER},
		{name: "other-open-unknown", openErr: fault},
		{name: "query-unknown", queryErr: fault},
		{name: "close-unknown", closeErr: fault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries, closes := 0, 0
			ownerOpenProcess = func(access uint32, inherit bool, pid uint32) (windows.Handle, error) {
				if access != windows.PROCESS_QUERY_LIMITED_INFORMATION || inherit || pid != 4242 {
					t.Fatalf("unsafe process handle request: %d %t %d", access, inherit, pid)
				}
				if tc.openErr != nil {
					return 0, tc.openErr
				}
				return 42, nil
			}
			ownerExitCode = func(h windows.Handle, code *uint32) error {
				queries++
				if h != 42 {
					t.Fatalf("queried unexpected handle %v", h)
				}
				*code = tc.code
				return tc.queryErr
			}
			ownerCloseHandle = func(h windows.Handle) error {
				closes++
				if h != 42 {
					t.Fatalf("closed unexpected handle %v", h)
				}
				return tc.closeErr
			}
			dead, err := platformProcessDead(4242)
			wantCalls := 1
			if tc.openErr != nil {
				wantCalls = 0
			}
			wantErr := errors.Join(tc.openErr, tc.queryErr, tc.closeErr)
			if dead != tc.dead || queries != wantCalls || closes != wantCalls ||
				(err == nil) != (wantErr == nil) || (wantErr != nil && !errors.Is(err, firstError(tc.openErr, tc.queryErr, tc.closeErr))) {
				t.Fatalf("dead=%t err=%v query=%d close=%d", dead, err, queries, closes)
			}
		})
	}
	for _, h := range []windows.Handle{0, windows.InvalidHandle} {
		ownerOpenProcess = func(uint32, bool, uint32) (windows.Handle, error) { return h, nil }
		ownerExitCode = func(windows.Handle, *uint32) error {
			t.Fatal("invalid handle was queried")
			return nil
		}
		ownerCloseHandle = func(windows.Handle) error {
			t.Fatal("invalid handle was closed")
			return nil
		}
		if dead, err := platformProcessDead(4242); dead || err == nil {
			t.Fatalf("invalid handle authorized death: %t %v", dead, err)
		}
	}
	ownerOpenProcess = func(uint32, bool, uint32) (windows.Handle, error) {
		t.Fatal("invalid PID reached OpenProcess")
		return 0, nil
	}
	large := int64(maxOwnerPID) + 1
	for _, pid := range []int{0, -1, int(large)} {
		if dead, err := platformProcessDead(pid); dead || err == nil {
			t.Fatalf("invalid pid %d: dead=%t err=%v", pid, dead, err)
		}
	}
	ownerSelfPID = func() int { return 4242 }
	if dead, err := platformProcessDead(4242); dead || err != nil {
		t.Fatalf("the executing owner must be retained without OpenProcess: %t %v", dead, err)
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func TestSnapshotOwnerWindowsPrivateACL(t *testing.T) {
	user, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		sid   *windows.SID
		mask  uint32
		flags byte
	}
	encode := func(entries ...entry) []byte {
		t.Helper()
		if len(entries) > 0xffff {
			t.Fatal("synthetic ACL entry count exceeds the native header")
		}
		data := make([]byte, 8)
		data[0] = 2
		binary.LittleEndian.PutUint16(data[4:6], uint16(len(entries)))
		for _, e := range entries {
			sid := unsafe.Slice((*byte)(unsafe.Pointer(e.sid)), e.sid.Len())
			ace := make([]byte, 8+len(sid))
			if len(ace) > 0xffff {
				t.Fatal("synthetic ACE exceeds the native header")
			}
			ace[0], ace[1] = windows.ACCESS_ALLOWED_ACE_TYPE, e.flags
			binary.LittleEndian.PutUint16(ace[2:4], uint16(len(ace)))
			binary.LittleEndian.PutUint32(ace[4:8], e.mask)
			copy(ace[8:], sid)
			data = append(data, ace...)
		}
		if len(data) > 0xffff {
			t.Fatal("synthetic ACL exceeds the native header")
		}
		binary.LittleEndian.PutUint16(data[2:4], uint16(len(data)))
		return data
	}
	const (
		full    = 0x1f01ff
		inherit = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		only    = inherit | windows.INHERIT_ONLY_ACE
	)
	u, s := entry{user, full, 0}, entry{system, full, 0}
	ui, si := entry{user, windows.GENERIC_ALL, only}, entry{system, windows.GENERIC_ALL, only}
	valid := func() []byte { return encode(u, ui, s, si) }
	for _, tc := range []struct {
		name      string
		data      []byte
		directory bool
		want      bool
	}{
		{"effective-and-inherit-only-four", valid(), true, true},
		{"principal-order-is-not-identity", encode(si, s, ui, u), true, true},
		{"generic-two", encode(entry{user, windows.GENERIC_ALL, inherit}, entry{system, windows.GENERIC_ALL, inherit}), true, true},
		{"effective-two", encode(u, s), true, true},
		{"file-effective-two", encode(u, s), false, true},
		{"file-inherited-effective", encode(entry{user, full, windows.INHERITED_ACE}, entry{system, full, windows.INHERITED_ACE}), false, true},
		{"foreign-effective", encode(u, s, entry{foreign, full, 0}), true, false},
		{"foreign-inherit-only", encode(u, s, entry{foreign, windows.GENERIC_ALL, only}), true, false},
		{"missing-user-effective", encode(ui, s, si), true, false},
		{"missing-system-effective", encode(u, ui, si), true, false},
		{"only-inherit-is-not-effective", encode(ui, si), true, false},
		{"inherit-only-on-file", valid(), false, false},
		{"zero-access", encode(entry{user, 0, 0}, s), true, false},
		{"partial-access", encode(entry{user, windows.GENERIC_READ, 0}, s), true, false},
		{"unknown-access-bits", encode(entry{user, full | 0x02000000, 0}, s), true, false},
		{"empty-acl", encode(), true, false},
		{"nil-acl", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerPrivateACL(tc.data, user, system, tc.directory); got != tc.want {
				t.Fatalf("private ACL=%t, want %t", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"unsupported-acl-revision", func(b []byte) { b[0] = 1 }},
		{"reserved-acl-byte", func(b []byte) { b[1] = 1 }},
		{"reserved-acl-word", func(b []byte) { b[6] = 1 }},
		{"acl-size-overrun", func(b []byte) { binary.LittleEndian.PutUint16(b[2:4], binary.LittleEndian.Uint16(b[2:4])+4) }},
		{"acl-count-overrun", func(b []byte) { binary.LittleEndian.PutUint16(b[4:6], 0xffff) }},
		{"unaccounted-ace", func(b []byte) { binary.LittleEndian.PutUint16(b[4:6], 2) }},
		{"denied-ace", func(b []byte) { b[8] = windows.ACCESS_DENIED_ACE_TYPE }},
		{"object-ace-unsupported", func(b []byte) { b[8] = 5 }},
		{"unknown-flags", func(b []byte) { b[9] = 0x40 }},
		{"no-propagate-unsupported", func(b []byte) { b[9] = inherit | windows.NO_PROPAGATE_INHERIT_ACE }},
		{"inherit-only-without-inheritance", func(b []byte) { b[9] = windows.INHERIT_ONLY_ACE }},
		{"short-ace", func(b []byte) { binary.LittleEndian.PutUint16(b[10:12], 8) }},
		{"unaligned-ace", func(b []byte) { binary.LittleEndian.PutUint16(b[10:12], 17) }},
		{"ace-overrun", func(b []byte) { binary.LittleEndian.PutUint16(b[10:12], 0xfffc) }},
		{"unsupported-sid-revision", func(b []byte) { b[16] = 2 }},
		{"sid-subauthority-overrun", func(b []byte) { b[17] = 15 }},
		{"sid-trailing-data", func(b []byte) { b[17]-- }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := valid()
			tc.mutate(data)
			if ownerPrivateACL(data, user, system, true) {
				t.Fatal("malformed/unsupported ACL admitted")
			}
		})
	}
	t.Run("missing-final-ace-after-complete-grants", func(t *testing.T) {
		data := valid()
		binary.LittleEndian.PutUint16(data[4:6], 5)
		before := string(data)
		if ownerPrivateACL(data, user, system, true) {
			t.Fatal("complete grant prefix hid a missing declared final ACE")
		}
		if string(data) != before {
			t.Fatal("refusing a truncated ACL changed its bytes")
		}
	})
	t.Run("checked-allocation", func(t *testing.T) {
		t.Run("allocation-too-short", func(t *testing.T) {
			short := new(uint16)
			*short = 0xa5a5
			var pinner runtime.Pinner
			pinner.Pin(short)
			defer pinner.Unpin()
			base := unsafe.Pointer(short)
			if ownerACLInAllocation(base, uint64(unsafe.Sizeof(*short)), uintptr(base), user, system, true) || *short != 0xa5a5 {
				t.Fatal("short owned allocation was admitted or mutated")
			}
			runtime.KeepAlive(short)
		})
		for _, tc := range []struct {
			name                              string
			prefix                            int
			acl                               []byte
			shortHeader, overrunHeader        bool
			beforeBase, insufficientRemaining bool
			want                              bool
		}{
			{name: "valid-base-grants", acl: valid(), want: true},
			{name: "valid-prefix-grants", prefix: 16, acl: valid(), want: true},
			{name: "missing-system-effective", prefix: 16, acl: encode(u, ui, si)},
			{name: "short-ACL-header", prefix: 16, acl: valid(), shortHeader: true},
			{name: "overrun-ACL-header", prefix: 16, acl: valid(), overrunHeader: true},
			{name: "candidate-before-base", prefix: 16, acl: valid(), beforeBase: true},
			{name: "candidate-last-two-bytes", prefix: 16, acl: valid(), insufficientRemaining: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				wordSize := int(unsafe.Sizeof(uintptr(0)))
				words := make([]uintptr, (tc.prefix+len(tc.acl)+8+wordSize-1)/wordSize)
				var pinner runtime.Pinner
				pinner.Pin(&words[0])
				defer pinner.Unpin()
				base := unsafe.Pointer(&words[0])
				data := unsafe.Slice((*byte)(base), len(words)*wordSize)
				for i := 0; i < tc.prefix; i++ {
					data[i] = 0xa5
				}
				copy(data[tc.prefix:], tc.acl)
				if tc.shortHeader {
					binary.LittleEndian.PutUint16(data[tc.prefix+2:tc.prefix+4], 4)
				}
				if tc.overrunHeader {
					length := len(data) - tc.prefix + 4
					if length > 0xffff {
						t.Fatal("synthetic ACL length exceeds its header")
					}
					binary.LittleEndian.PutUint16(data[tc.prefix+2:tc.prefix+4], uint16(length))
				}
				address := uintptr(base) + uintptr(tc.prefix)
				if tc.beforeBase {
					address = uintptr(base) - 4
				}
				if tc.insufficientRemaining {
					address = uintptr(base) + uintptr(len(data)-2)
				}
				before := string(data)
				if got := ownerACLInAllocation(base, uint64(len(data)), address, user, system, true); got != tc.want {
					t.Fatalf("checked allocation admitted=%t, want %t", got, tc.want)
				}
				if string(data) != before {
					t.Fatal("checked allocation parser changed owned bytes")
				}
				runtime.KeepAlive(words)
			})
		}
	})
	if !ownerPrivateACL(encode(s), system, system, true) {
		t.Fatal("current user=SYSTEM must not require a second distinct principal")
	}
	if ownerPrivateACL(valid(), nil, system, true) || ownerPrivateACL(valid(), user, nil, true) {
		t.Fatal("unknown intended principal admitted")
	}
}

func TestSnapshotOwnerWindowsProtectionFailures(t *testing.T) {
	get, token, systemSID := ownerGetSecurity, ownerTokenUser, ownerWellKnownSID
	t.Cleanup(func() {
		ownerGetSecurity, ownerTokenUser, ownerWellKnownSID = get, token, systemSID
	})
	const userText = "S-1-5-21-1-2-3-1001"
	user, err := windows.StringToSid(userText)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := func(sddl string) *windows.SECURITY_DESCRIPTOR {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil || sd == nil || !sd.IsValid() {
			t.Fatalf("invalid synthetic descriptor fixture: %v", err)
		}
		return sd
	}
	acl := "(A;;GA;;;" + userText + ")(A;;GA;;;SY)"
	valid := descriptor("O:" + userText + "D:P" + acl)
	noOwner := descriptor("D:P" + acl)
	systemOwner := descriptor("O:SYD:P" + acl)
	unprotected := descriptor("O:" + userText + "D:" + acl)
	noDACL := descriptor("O:" + userText)
	if err := noDACL.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		t.Fatal(err)
	}
	if owner, _, err := noOwner.Owner(); err != nil || owner != nil {
		t.Fatalf("owner-absent fixture not qualified: %v", err)
	}
	if _, _, err := noDACL.DACL(); !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		t.Fatalf("DACL-absent fixture not qualified: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unchanged")
	const payload = "owned protection-query fixture"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("synthetic native metadata uncertainty")
	for _, tc := range []struct {
		name                          string
		sd                            *windows.SECURITY_DESCRIPTOR
		queryErr, tokenErr, systemErr error
		file, want                    bool
	}{
		{name: "private-current-owner", sd: valid, want: true},
		{name: "query-error", queryErr: fault},
		{name: "descriptor-absent"},
		{name: "owner-absent", sd: noOwner},
		{name: "token-error", sd: valid, tokenErr: fault},
		{name: "owner-not-current-user", sd: systemOwner},
		{name: "system-SID-error", sd: valid, systemErr: fault},
		{name: "unprotected-directory", sd: unprotected},
		{name: "DACL-absent", sd: noDACL},
		{name: "unprotected-file", sd: unprotected, file: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantPath, info := dir, dirInfo
			if tc.file {
				wantPath, info = path, fileInfo
			}
			calls := 0
			ownerGetSecurity = func(p string, kind windows.SE_OBJECT_TYPE, flags windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
				calls++
				if p != wantPath || kind != windows.SE_FILE_OBJECT ||
					flags != windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION {
					t.Fatalf("unsafe native metadata query: %q %d %d", p, kind, flags)
				}
				return tc.sd, tc.queryErr
			}
			ownerTokenUser = func() (*windows.Tokenuser, error) {
				if tc.tokenErr != nil {
					return nil, tc.tokenErr
				}
				return &windows.Tokenuser{User: windows.SIDAndAttributes{Sid: user}}, nil
			}
			ownerWellKnownSID = func(kind windows.WELL_KNOWN_SID_TYPE) (*windows.SID, error) {
				if kind != windows.WinLocalSystemSid {
					t.Fatalf("queried an unrelated well-known principal: %d", kind)
				}
				if tc.systemErr != nil {
					return nil, tc.systemErr
				}
				return systemSID(kind)
			}
			if got := platformProtected(wantPath, info); got != tc.want || calls != 1 {
				t.Fatalf("protection=%t want=%t metadata calls=%d", got, tc.want, calls)
			}
		})
	}
	after, err := os.ReadFile(path) //nolint:gosec // G304: exact owned file written above, proving metadata refusals preserved its bytes
	if err != nil || string(after) != payload {
		t.Fatalf("protection query changed file contents: %q %v", after, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "unchanged" {
		t.Fatalf("protection query changed the owned directory: %v %v", entries, err)
	}
}

func TestSnapshotOwnerWindowsOpenMarkerEmbeddedNUL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, markerName) + "\x00suffix"
	f, err := platformOpenMarker(path)
	if f != nil {
		_ = f.Close()
		t.Fatal("embedded NUL unexpectedly opened a marker")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("lost embedded-NUL conversion cause: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid marker path mutated its owned directory: %v %v", entries, err)
	}
}
