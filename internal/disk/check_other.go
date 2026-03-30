//go:build !windows

package disk

import (
	"fmt"
	"syscall"
)

// Check returns disk usage for the drive containing path using statfs(2).
func Check(path string) (Info, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Info{}, fmt.Errorf("statfs %s: %w", path, err)
	}

	const gb = 1024 * 1024 * 1024
	totalGB := float64(stat.Blocks) * float64(stat.Bsize) / gb
	freeGB := float64(stat.Bavail) * float64(stat.Bsize) / gb
	freePct := 0.0
	if totalGB > 0 {
		freePct = freeGB / totalGB * 100
	}

	return Info{
		Path:    path,
		TotalGB: totalGB,
		FreeGB:  freeGB,
		FreePct: freePct,
	}, nil
}

// WarnDialog logs a low disk space warning.
func WarnDialog(info Info, warnThresholdPct int) {
	fmt.Printf("owlrun: low disk space: %.1f GB free (%.0f%%) on %s\n",
		info.FreeGB, info.FreePct, info.Path)
}

// CriticalDialog logs a critical disk space error.
func CriticalDialog(info Info, minSpaceGB float64) {
	fmt.Printf("owlrun: critical disk space: need %.0f GB, have %.1f GB on %s\n",
		minSpaceGB, info.FreeGB, info.Path)
}
