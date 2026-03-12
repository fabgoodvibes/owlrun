//go:build !windows

package tray

import (
	"os/exec"
	"runtime"
)

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start() //nolint:errcheck
	default:
		// gio open is more reliable on GNOME/Ubuntu; fall back to xdg-open.
		if exec.Command("gio", "open", url).Start() != nil {
			exec.Command("xdg-open", url).Start() //nolint:errcheck
		}
	}
}
