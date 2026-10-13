//go:build darwin || linux

package sourcecapture

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLockSQLiteFaultsReleaseOnlyOwnedLocks(t *testing.T) {
	for _, failCall := range []int{1, 2, 3, 4} {
		t.Run(strconv.Itoa(failCall), func(t *testing.T) {
			path := sqliteFixture(t)
			file, err := os.Open(path) //nolint:gosec // synthetic fixture
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			original := fcntlLock
			t.Cleanup(func() { fcntlLock = original })
			calls := 0
			var ranges []string
			fcntlLock = func(fd uintptr, cmd int, lock *unix.Flock_t) error {
				calls++
				kind := "U"
				if lock.Type == unix.F_RDLCK {
					kind = "R"
				}
				ranges = append(ranges, kind+"@"+strconv.FormatInt(lock.Start, 10)+"+"+strconv.FormatInt(lock.Len, 10))
				if calls == failCall {
					return errors.New("lock fault")
				}
				return original(fd, cmd, lock)
			}
			unlock, err := LockSQLite(file)
			if failCall == 4 {
				if err != nil {
					t.Fatal(err)
				}
				if err := unlock(); err == nil {
					t.Fatal("unlock failure hidden")
				}
			} else if err == nil || unlock != nil {
				t.Fatal("lock failure admitted")
			}
			want := map[int]string{
				1: "R@1073741824+1",
				2: "R@1073741824+1,R@1073741826+510,U@1073741824+1",
				3: "R@1073741824+1,R@1073741826+510,U@1073741824+1,U@1073741824+1,U@1073741826+510",
				4: "R@1073741824+1,R@1073741826+510,U@1073741824+1,U@1073741826+510",
			}
			if got := strings.Join(ranges, ","); got != want[failCall] {
				t.Fatalf("lock protocol %s, want %s", got, want[failCall])
			}
			fcntlLock = original
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if output := writerProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("lock leak after fault: %s", output)
			}
		})
	}
}
