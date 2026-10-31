//go:build !unix && !windows

package radiolock

import (
	"errors"
	"os"
)

func lock(*os.File) error { return errors.ErrUnsupported }
