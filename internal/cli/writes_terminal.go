package cli

import (
	"errors"
	"io"
	"os"
	goruntime "runtime"
)

// controllingTerminal opens the person's terminal itself, never standard input: /dev/tty, or the
// Windows console's input and output.
func controllingTerminal() (io.ReadWriteCloser, error) {
	if goruntime.GOOS == "windows" {
		in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			return nil, errors.Join(err, in.Close())
		}
		return console{in: in, out: out}, nil
	}
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// console reads from one handle and writes to another, as the Windows console needs.
type console struct{ in, out *os.File }

func (c console) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c console) Write(p []byte) (int, error) { return c.out.Write(p) }
func (c console) Close() error                { return errors.Join(c.in.Close(), c.out.Close()) }
