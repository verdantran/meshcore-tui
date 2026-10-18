package privfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "f")
	if err := Write(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("read %q", b)
	}
	if err := WriteWith(path, func(w io.Writer) error { return errors.New("half way") }); err == nil {
		t.Error("a failed write")
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("a failed write left %q", b)
	}
	if left, _ := filepath.Glob(path + ".*"); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
}

func TestDirRefusesFoldersOthersControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no owner or mode bits to check")
	}
	open := t.TempDir()
	os.Chmod(open, 0o777)
	if err := Write(filepath.Join(open, "f"), nil); err == nil || !strings.Contains(err.Error(), "others can write") {
		t.Errorf("a folder anyone can write to: %v", err)
	}
	os.Chmod(open, 0o770)
	if err := Dir(open); err == nil {
		t.Error("a folder its group can write to")
	}
	if os.Getuid() != 0 {
		if err := Dir("/"); err == nil || !strings.Contains(err.Error(), "another user") {
			t.Errorf("root's folder: %v", err)
		}
	}
	if err := Dir(t.TempDir()); err != nil {
		t.Errorf("our own folder: %v", err)
	}
}
