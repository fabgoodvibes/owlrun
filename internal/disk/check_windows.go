package disk

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	user32              = syscall.NewLazyDLL("user32.dll")
	getDiskFreeSpaceEx  = kernel32.NewProc("GetDiskFreeSpaceExW")
	messageBoxW         = user32.NewProc("MessageBoxW")
)

// Check returns disk usage for the drive containing path.
// Uses GetDiskFreeSpaceExW so it correctly handles drive quotas and
// reports the space available to the calling user (not total free).
func Check(path string) (Info, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Info{}, err
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r1, _, e := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return Info{}, fmt.Errorf("GetDiskFreeSpaceExW: %w", e)
	}

	const gb = 1024 * 1024 * 1024
	totalGB := float64(totalBytes) / gb
	freeGB := float64(freeBytesAvailable) / gb
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

// WarnDialog shows a Windows MessageBox warning about low disk space.
// Used at startup when space is below the warn threshold.
func WarnDialog(info Info, warnThresholdPct int) {
	msg := fmt.Sprintf(
		"Owlrun detected low disk space on %s.\n\n"+
			"Free: %.1f GB (%.0f%% of %.0f GB)\n"+
			"Recommended minimum: %d%% free for model downloads.\n\n"+
			"Owlrun will continue, but model downloads may fail.\n"+
			"Consider freeing up space or setting a different model drive.",
		info.Path, info.FreeGB, info.FreePct, info.TotalGB, warnThresholdPct,
	)
	showMsgBox("Owlrun — Low Disk Space", msg, 0x30) // MB_OK | MB_ICONWARNING
}

// CriticalDialog shows a blocking MessageBox when space is too low to
// download even the smallest model. Returns after user clicks OK.
func CriticalDialog(info Info, minSpaceGB float64) {
	msg := fmt.Sprintf(
		"Owlrun needs at least %.0f GB of free disk space to download AI models.\n\n"+
			"You currently have %.1f GB free on %s.\n\n"+
			"Please free up disk space, then restart Owlrun.",
		minSpaceGB, info.FreeGB, info.Path,
	)
	showMsgBox("Owlrun — Insufficient Disk Space", msg, 0x10) // MB_OK | MB_ICONERROR
}

func showMsgBox(title, msg string, flags uint32) {
	msgPtr, _ := syscall.UTF16PtrFromString(msg)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	messageBoxW.Call(0,
		uintptr(unsafe.Pointer(msgPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(flags),
	)
}
