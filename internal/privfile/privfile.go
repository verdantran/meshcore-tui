// Package privfile writes files only their owner can read, into folders no one else can change.
package privfile

import (
	"io"
	"os"
	"path/filepath"
)

// Write replaces path with data, atomically.
func Write(path string, data []byte) error {
	return WriteWith(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteWith replaces path with what write writes. The temp file is always new and 0600, so neither a stale
// file's permissions nor a symlink planted in its place can carry the contents anywhere else.
func WriteWith(path string, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := Dir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Dir makes dir if need be, and refuses one that another user owns or others can write to.
func Dir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	return check(dir, fi)
}
