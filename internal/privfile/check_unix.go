//go:build unix

package privfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func check(dir string, fi fs.FileInfo) error {
	if !mine(fi) {
		return fmt.Errorf("%s belongs to another user, so it can't hold private files", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("others can write to %s, so it can't hold private files; chmod go-w it to use it", dir)
	}
	return nil
}

// parents refuses a folder above dir, up to the home folder, that someone else could rename dir out of:
// one that another user owns, or that others can write to without the sticky bit, as /tmp has.
func parents(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	stop := map[string]bool{}
	if home, err := os.UserHomeDir(); err == nil {
		stop[home] = true
		if h, err := filepath.EvalSymlinks(home); err == nil {
			stop[h] = true
		}
	}
	for _, p := range []string{abs, real} {
		for d := filepath.Dir(p); !stop[d]; d = filepath.Dir(d) {
			fi, err := os.Lstat(d)
			if err != nil {
				return err
			}
			if fi.Mode()&fs.ModeSymlink == 0 { // the link's own folder is checked next, its target in real
				if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && !mine(fi) {
					return fmt.Errorf("%s, above %s, belongs to another user, so it can't hold private files", d, dir)
				}
				if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&fs.ModeSticky == 0 {
					return fmt.Errorf("others can write to %s, above %s, so it can't hold private files", d, dir)
				}
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return nil
}

func own(path string, f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !mine(fi) {
		return fmt.Errorf("%s belongs to another user, so it isn't read", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return f.Chmod(0o600)
	}
	return nil
}

func mine(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}
