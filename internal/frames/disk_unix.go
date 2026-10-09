//go:build unix

package frames

import (
	"errors"
	"syscall"
)

// DiskFreePct reports free space as a percentage of the filesystem holding
// dir, using the non-reserved blocks (what the app can actually write).
// The deploy target is a Linux VPS (§8); this gate decides DEGRADED/507.
func DiskFreePct(dir string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	if total <= 0 {
		return 0, errors.New("statfs: zero-size filesystem")
	}
	avail := float64(st.Bavail) * float64(st.Bsize)
	return 100 * avail / total, nil
}
