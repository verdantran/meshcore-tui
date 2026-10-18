//go:build unix

package privfile

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func check(dir string, fi fs.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user, so nothing is saved there", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("others can write to %s, so nothing is saved there", dir)
	}
	return nil
}
