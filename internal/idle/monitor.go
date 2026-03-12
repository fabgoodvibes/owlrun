// Package idle detects whether the machine is in a state where Owlrun
// should be earning: no recent user input, GPU not busy, no game running.
package idle

import (
	"strings"
	"time"

	ps "github.com/mitchellh/go-ps"

	"github.com/fabgoodvibes/owlrun/internal/config"
)

// IsSystemIdle returns true when all three idle conditions are satisfied:
//  1. No keyboard/mouse input for at least cfg.TriggerMinutes
//  2. GPU utilisation (gpuUtilPct) is below cfg.GPUThreshold
//  3. No known game process is running (if cfg.WatchProcesses is true)
//
// gpuUtilPct is supplied by the gpu.Monitor so we have a single nvidia-smi
// caller rather than two independent ones.
func IsSystemIdle(cfg config.IdleConfig, gpuUtilPct int) bool {
	threshold := time.Duration(cfg.TriggerMinutes) * time.Minute
	if IdleDuration() < threshold {
		return false
	}
	if gpuUtilPct >= cfg.GPUThreshold {
		return false
	}
	if cfg.WatchProcesses && IsGameRunning() {
		return false
	}
	return true
}

// IsGameRunning scans the live process list for known game executables.
func IsGameRunning() bool {
	procs, err := ps.Processes()
	if err != nil {
		return false
	}
	for _, p := range procs {
		name := strings.ToLower(p.Executable())
		for _, game := range KnownGameExes {
			if name == game {
				return true
			}
		}
	}
	return false
}

// KnownGameExes is the list of process names (lowercase) that indicate a
// game is running. Covers launchers and common high-GPU titles.
// Add entries here as the community reports false negatives.
var KnownGameExes = []string{
	// Launchers
	"steam.exe", "steamwebhelper.exe",
	"epicgameslauncher.exe",
	"battle.net.exe", "agent.exe",
	"galaxyclient.exe",
	"upc.exe", "ubisoftconnect.exe",
	"origin.exe", "eadesktop.exe",
	"riotclientservices.exe",
	"bethesdanetlauncher.exe",
	"xboxapp.exe", "gamingservices.exe",

	// Competitive / popular titles
	"cs2.exe", "csgo.exe",
	"valorant.exe", "valorant-win64-shipping.exe",
	"r5apex.exe",
	"fortnite.exe", "fortniteclient-win64-shipping.exe",
	"cod.exe", "modernwarfare.exe", "warzone.exe",
	"overwatch.exe", "overwatch2.exe",
	"dota2.exe",
	"rocketleague.exe",
	"leagueoflegends.exe",
	"pathofexile.exe",
	"diablo4.exe",
	"starcraft2.exe", "sc2.exe",
	"hearthstone.exe",
	"pubg.exe", "tslgame.exe",
	"destiny2.exe",

	// Open-world / AAA
	"eldenring.exe", "sekiro.exe",
	"cyberpunk2077.exe",
	"witcher3.exe",
	"gtav.exe", "gta5.exe",
	"rdrii.exe", "rdr2.exe",
	"minecraft.exe", "minecraftlauncher.exe",
	"thefinals.exe",
	"hunt.exe",
	"deadbydaylight.exe",
	"escapefromtarkov.exe",
}
