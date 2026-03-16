//go:build !windows

package idle

import "time"

// IdleDuration returns how long the machine has been idle.
// On Linux/macOS servers there is no user input to track — the node is
// always considered past the idle threshold so earning starts immediately.
func IdleDuration() time.Duration { return 24 * time.Hour }
