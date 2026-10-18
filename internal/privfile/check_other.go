//go:build !unix

package privfile

import (
	"io/fs"
	"os"
)

// Windows has no owner or mode bits to check here: the folder is private because it's under the user's profile.
func check(string, fs.FileInfo) error { return nil }

func parents(string) error { return nil }

func own(string, *os.File) error { return nil }
