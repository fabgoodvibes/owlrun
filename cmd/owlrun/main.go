// Owlrun — idle GPU earning agent.
// Runs silently in the system tray. Earns money when your machine is idle.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/fabgoodvibes/owlrun/internal/config"
	"github.com/fabgoodvibes/owlrun/internal/dashboard"
	"github.com/fabgoodvibes/owlrun/internal/tray"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("owlrun", version)
		os.Exit(0)
	}

	// Load config from ~/.owlrun/owlrun.conf (defaults used if file absent).
	cfg, err := config.Load()
	if err != nil {
		log.Printf("owlrun: config error: %v — using defaults", err)
	}

	// Start the local dashboard server (port 8080).
	dash := dashboard.New(8080)
	if err := dash.Start(); err != nil {
		log.Printf("owlrun: dashboard failed to start: %v", err)
	}

	// Run the system tray — blocks until the user clicks Quit.
	tray.Run(cfg, dash)
}
