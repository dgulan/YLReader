//go:build linux

package browserassist

import (
	"os"
	"path/filepath"
	"syscall"
)

func privateRelaySocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrScope
	}
	parent := filepath.Dir(path)
	for p := parent; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return ErrScope
		}
		if p == parent {
			sys, ok := st.Sys().(*syscall.Stat_t)
			if !ok || sys.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0700 {
				return ErrScope
			}
		}
		if p == "/" {
			break
		}
	}
	st, e := os.Lstat(path)
	if e != nil || st.Mode()&os.ModeSocket == 0 || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0600 {
		return ErrScope
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) {
		return ErrScope
	}
	return nil
}
