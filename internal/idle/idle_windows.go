package idle

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// IdleDuration returns how long the machine has had no keyboard or mouse input.
func IdleDuration() time.Duration {
	var info lastInputInfo
	info.cbSize = uint32(unsafe.Sizeof(info))
	procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))

	now, _, _ := procGetTickCount.Call()
	idleMs := uint32(now) - info.dwTime
	return time.Duration(idleMs) * time.Millisecond
}
