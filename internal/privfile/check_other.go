//go:build !unix

package privfile

import "io/fs"

// Windows has no owner or mode bits to check here: the folder is private because it's under the user's profile.
func check(string, fs.FileInfo) error { return nil }
