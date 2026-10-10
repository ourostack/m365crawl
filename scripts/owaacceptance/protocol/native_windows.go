package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type BinaryPin struct {
	file   *os.File
	Hash   string
	Native [3]uint32
}

const MaxBinaryBytes = 128 << 20

func SelectedTest() error {
	if len(os.Args) != 3 || os.Args[1] != "-test.run=^"+TestName+"$" || os.Args[2] != "-test.timeout=4m" {
		return refused()
	}
	return nil
}

func SafeAbsolute(path string, directory bool, noGit bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return refused()
	}
	for current := path; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return refused()
		}
		attr, err := windows.GetFileAttributes(name)
		if err != nil || attr&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (current != path || directory) && attr&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return refused()
		}
		if noGit {
			// #nosec G703 -- Read-only Git-ancestor refusal on the clean absolute non-reparse path validated above.
			_, err := os.Lstat(filepath.Join(current, ".git"))
			if !errors.Is(err, os.ErrNotExist) {
				return refused()
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func NativeIdentity(path string, directory bool) ([3]uint32, error) {
	var id [3]uint32
	if SafeAbsolute(path, directory, false) != nil {
		return id, refused()
	}
	name, _ := windows.UTF16PtrFromString(path)
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return id, refused()
	}
	var info windows.ByHandleFileInformation
	readErr := windows.GetFileInformationByHandle(handle, &info)
	closeErr := windows.CloseHandle(handle)
	if readErr != nil || closeErr != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || directory != (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
		return id, refused()
	}
	return [3]uint32{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, nil
}

func privateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, refused()
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")(A;" + flags + ";FA;;;SY)")
	if err != nil {
		return nil, refused()
	}
	return sd, nil
}

func MakePrivateDirectory(path string) error {
	sd, err := privateDescriptor(true)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return refused()
	}
	if windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil) != nil {
		return refused()
	}
	return CheckPrivate(path, true)
}

func CheckPrivate(path string, directory bool) error {
	if SafeAbsolute(path, directory, true) != nil {
		return refused()
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return refused()
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return refused()
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return refused()
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return refused()
	}
	seen := map[string]bool{}
	wantFlags := byte(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	for i := uint32(0); i < 2; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != windows.ACCESS_MASK(0x001f01ff) || ace.Header.AceFlags != wantFlags {
			return refused()
		}
		// #nosec G103 -- GetAce supplies the SDK variable-length allowed ACE; SidStart is the validated inline SID.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != user.User.Sid.String() && sid != "S-1-5-18" {
			return refused()
		}
		seen[sid] = true
	}
	if !seen[user.User.Sid.String()] || !seen["S-1-5-18"] {
		return refused()
	}
	return nil
}

func PinBinary(path string, expected string) (*BinaryPin, error) {
	if !hash(expected) || SafeAbsolute(path, false, false) != nil {
		return nil, refused()
	}
	name, _ := windows.UTF16PtrFromString(path)
	// Read-only sharing pins both reviewed bytes and the directory entry against ordinary write/delete replacement through the run.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, refused()
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if file == nil {
		if windows.CloseHandle(handle) != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	if windows.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		if file.Close() != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	if uint64(info.FileSizeHigh)<<32|uint64(info.FileSizeLow) > MaxBinaryBytes {
		if file.Close() != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, file)
	actual := hex.EncodeToString(digest.Sum(nil))
	if readErr != nil || actual != expected {
		if file.Close() != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	return &BinaryPin{file: file, Hash: actual, Native: [3]uint32{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}}, nil
}

func (pin *BinaryPin) Close() error {
	if pin == nil || pin.file == nil {
		return nil
	}
	err := pin.file.Close()
	pin.file = nil
	if err != nil {
		return refused()
	}
	return nil
}

func SelfBinary(expected string) ([3]uint32, error) {
	var id [3]uint32
	path, err := os.Executable()
	if err != nil {
		return id, refused()
	}
	pin, err := PinBinary(path, expected)
	if err != nil {
		return id, err
	}
	id = pin.Native
	if pin.Close() != nil {
		return [3]uint32{}, refused()
	}
	return id, nil
}

func ProcessIdentity(pid uint32) (Process, error) {
	if pid == 0 {
		return Process{}, refused()
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return Process{}, refused()
	}
	var created, exited, kernel, user windows.Filetime
	timeErr := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
	state, waitErr := windows.WaitForSingleObject(handle, 0)
	closeErr := windows.CloseHandle(handle)
	if timeErr != nil || waitErr != nil || closeErr != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return Process{}, refused()
	}
	return Process{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, nil
}

func ReadProtected(path string) ([]byte, error) {
	if CheckPrivate(filepath.Dir(path), true) != nil || CheckPrivate(path, false) != nil {
		return nil, refused()
	}
	name, _ := windows.UTF16PtrFromString(path)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, refused()
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		if windows.CloseHandle(handle) != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || CheckPrivate(path, false) != nil {
		if file.Close() != nil {
			return nil, refused()
		}
		return nil, refused()
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > MaxRecordBytes {
		return nil, refused()
	}
	return data, nil
}

func WriteProtectedNew(path string, data []byte) error {
	if len(data) == 0 || len(data) > MaxRecordBytes || CheckPrivate(filepath.Dir(path), true) != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "" {
		return refused()
	}
	sd, err := privateDescriptor(false)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return refused()
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return refused()
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		if windows.CloseHandle(handle) != nil {
			return refused()
		}
		return refused()
	}
	// Protection exists at creation, before the first durable record byte.
	aclErr := CheckPrivate(path, false)
	var writeErr, syncErr error
	if aclErr == nil {
		_, writeErr = file.Write(data)
		if writeErr == nil {
			syncErr = file.Sync()
		}
	}
	closeErr := file.Close()
	if aclErr != nil || writeErr != nil || syncErr != nil || closeErr != nil {
		return refused()
	}
	return nil
}

func CurrentGuard() (Ownership, error) {
	if SelectedTest() != nil {
		return Ownership{}, refused()
	}
	path := os.Getenv(OwnerEnv)
	if filepath.Base(path) != AdoptedName {
		return Ownership{}, refused()
	}
	data, err := ReadProtected(path)
	if err != nil {
		return Ownership{}, err
	}
	owner, err := DecodeOwnership(data)
	if err != nil || !owner.Adopted {
		return Ownership{}, refused()
	}
	initialData, err := ReadProtected(filepath.Join(filepath.Dir(path), OwnerName))
	if err != nil {
		return Ownership{}, refused()
	}
	initial, err := DecodeOwnership(initialData)
	if err != nil || initial.Adopted {
		return Ownership{}, refused()
	}
	wantInitial := owner
	wantInitial.Adopted = false
	wantInitial.Child = Process{}
	if initial != wantInitial || CheckPrivate(owner.Root, true) != nil || SafeAbsolute(owner.Source, true, true) != nil {
		return Ownership{}, refused()
	}
	if StorageOutsideSource(owner.Source, owner.Root, filepath.Dir(path)) != nil {
		return Ownership{}, refused()
	}
	root, err := NativeIdentity(owner.Root, true)
	if err != nil || root != owner.RootNative {
		return Ownership{}, refused()
	}
	pid, ppid := os.Getpid(), os.Getppid()
	if pid <= 0 || int64(pid) > 0xffffffff || ppid <= 0 || int64(ppid) > 0xffffffff {
		return Ownership{}, refused()
	}
	// #nosec G115 -- Both native PIDs are positive and within uint32 range immediately above.
	childPID, parentPID := uint32(pid), uint32(ppid)
	child, err := ProcessIdentity(childPID)
	if err != nil || child != owner.Child || parentPID != owner.Supervisor.PID {
		return Ownership{}, refused()
	}
	parent, err := ProcessIdentity(owner.Supervisor.PID)
	if err != nil || parent != owner.Supervisor {
		return Ownership{}, refused()
	}
	binary, err := SelfBinary(owner.BinaryHash)
	if err != nil || binary != owner.BinaryNative {
		return Ownership{}, refused()
	}
	for _, key := range []string{"TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE"} {
		if os.Getenv(key) != owner.Root {
			return Ownership{}, refused()
		}
	}
	if os.Getenv("LOCALAPPDATA") != filepath.Join(owner.Root, "AppData", "Local") || os.Getenv("APPDATA") != filepath.Join(owner.Root, "AppData", "Roaming") || os.Getenv("M365CRAWL_REAL_CACHE") != "1" || os.Getenv("M365CRAWL_OUTLOOK_ROOT") != owner.Source || os.Getenv("M365CRAWL_TEAMS_ROOT") != filepath.Join(owner.Root, "no-teams") {
		return Ownership{}, refused()
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "M365CRAWL_") && key != "M365CRAWL_REAL_CACHE" && key != "M365CRAWL_OUTLOOK_ROOT" && key != "M365CRAWL_TEAMS_ROOT" && key != OwnerEnv {
			return Ownership{}, refused()
		}
	}
	return owner, nil
}

func EmitQualification(owner Ownership, counts map[string]int64, observed, unstable int64) error {
	current, err := CurrentGuard()
	if err != nil || current != owner {
		return refused()
	}

	witness := Qualification{Version: Version, Nonce: owner.Nonce, Test: owner.Test, BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative, Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "passed", Observed: observed, Unstable: unstable, Counts: counts}
	if Match(owner, witness) != nil {
		return refused()
	}
	data, err := Encode(witness)
	if err != nil {
		return err
	}
	return WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(OwnerEnv)), WitnessName), data)
}

func EmitDiagnostic(owner Ownership, stage string, fields []string) error {
	current, err := CurrentGuard()
	if err != nil || current != owner {
		return refused()
	}
	d := DiagnosticFor(owner, stage, fields)
	if MatchDiagnostic(owner, d) != nil {
		return refused()
	}
	data, err := Encode(d)
	if err != nil {
		return err
	}
	return PublishDiagnostic(filepath.Dir(os.Getenv(OwnerEnv)), data)
}

func PublishDiagnostic(directory string, data []byte) error {
	if _, err := DecodeDiagnostic(data); err != nil {
		return refused()
	}
	pending := filepath.Join(directory, DiagnosticPendingName)
	if WriteProtectedNew(pending, data) != nil {
		return refused()
	}
	from, err := windows.UTF16PtrFromString(pending)
	if err != nil {
		return refused()
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(directory, DiagnosticName))
	if err != nil || CheckPrivate(directory, true) != nil || windows.MoveFile(from, to) != nil {
		return refused()
	}
	return nil
}
