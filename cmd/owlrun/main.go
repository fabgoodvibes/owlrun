// Owlrun — idle GPU earning agent.
// Runs silently in the system tray. Earns money when your machine is idle.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/config"
	"github.com/fabgoodvibes/owlrun/internal/dashboard"
	"github.com/fabgoodvibes/owlrun/internal/tray"
)

func hasCLIFlag(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}

func main() {
	if hasCLIFlag("--version") || hasCLIFlag("-v") {
		fmt.Printf("owlrun %s (%s)\n", buildinfo.Version, buildinfo.Network)
		os.Exit(0)
	}

	mockMode := hasCLIFlag("--mock")

	// Load config from ~/.owlrun/owlrun.conf (defaults used if file absent).
	cfg, err := config.Load()
	if err != nil {
		log.Printf("owlrun: config error: %v — using defaults", err)
	}

	if config.NeedsWallet(&cfg) {
		log.Printf("owlrun: WARNING — no payout wallet configured. Set your Lightning address at http://localhost:19131 or edit %s", config.Path())
	}

	// Start the local dashboard server (port 19131).
	dash := dashboard.New(19131)
	if err := dash.Start(); err != nil {
		log.Printf("owlrun: dashboard failed to start: %v", err)
	}

	if mockMode {
		log.Printf("owlrun: starting in MOCK MODE — no real Ollama required")
	}

	// Run the system tray — blocks until the user clicks Quit.
	tray.Run(cfg, dash, mockMode)
}
