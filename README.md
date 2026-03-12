# Owlrun

Earn money from your idle GPU by serving AI inference.

Owlrun is a lightweight agent that runs silently in your system tray. When your machine is idle, it serves AI inference jobs through the [Owlrun Gateway](https://owlrun.me) and earns you money. When you come back, it pauses automatically.

## Install

**Windows** (PowerShell):
```powershell
irm https://get.owlrun.me/install.ps1 | iex
```

**Linux / macOS** (bash):
```bash
curl -fsSL https://get.owlrun.me/install.sh | bash
```

The installer detects your GPU, installs [Ollama](https://ollama.com) if needed, downloads the Owlrun binary, writes a default config, and registers auto-start.

## How it works

```
Your machine                           Owlrun Gateway                    Buyer
+-----------+    WebSocket control    +----------------+    HTTPS API   +-------+
|  Owlrun   | ---------------------->|   gateway.     |<---------------|  App  |
|  + Ollama | <------- jobs ---------|   owlrun.me    |--- response -->|       |
+-----------+    HTTP/2 proxy         +----------------+               +-------+
```

1. Owlrun connects to the gateway over WebSocket and registers your GPU specs
2. When a buyer sends an inference request, the gateway pushes a job to your node
3. Your node fetches the buyer's request, forwards it to local Ollama, and streams the response back
4. You earn 85% of the job revenue; the gateway takes a 15% routing margin
5. Payouts are weekly on Solana

Your node **only** talks to the gateway — never directly to buyers.

## Requirements

- **GPU**: NVIDIA (any with CUDA), AMD (ROCm on Linux, WMI on Windows), or Apple Silicon
- **Disk**: 8 GB+ free (for AI model downloads)
- **OS**: Windows 10+, macOS 12+, or Linux (x86_64 / arm64)
- **Network**: outbound HTTPS + WSS to `gateway.owlrun.me`

CPU-only mode is supported for small models with lower earnings.

## Configuration

Config file: `~/.owlrun/owlrun.conf`

```ini
[account]
api_key  = owlr_prov_...          # From https://dashboard.owlrun.me
wallet   = <solana-address>       # Payout address

[marketplace]
gateway        = https://gateway.owlrun.me
region         = auto             # auto-detected from IP, or set manually

[inference]
model_auto     = true             # Auto-select best model for your VRAM
max_vram_pct   = 80

[idle]
trigger_minutes = 10              # Start earning after 10 min idle
gpu_threshold   = 15              # Only earn if GPU usage < 15%
watch_processes = true            # Pause when games are running

[disk]
warn_threshold_pct = 30
min_model_space_gb = 8
```

See [`owlrun.conf.example`](owlrun.conf.example) for all options.

## Dashboard

Once running, open [http://localhost:8080](http://localhost:8080) for a live dashboard showing GPU stats, earnings, gateway status, and disk usage.

## Build from source

Requires Go 1.22+.

```bash
# Native build
make build

# All platforms
make build-all

# Run tests
make test
```

Or manually:

```bash
CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=dev" -o dist/owlrun ./cmd/owlrun
```

Cross-compile for Windows from Linux/macOS:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o dist/owlrun.exe ./cmd/owlrun
```

## Project structure

```
owlrun/
  cmd/owlrun/           Entry point
  internal/
    config/             INI config loader
    tray/               System tray UI (Windows, Linux, macOS)
    idle/               Idle detection + game scanner
    gpu/                GPU detection and live monitoring
    disk/               Disk space checks
    inference/          Ollama lifecycle manager
    earnings/           SQLite earnings tracker
    marketplace/        Gateway connector (WebSocket + HTTP/2 proxy)
    dashboard/          Local web UI on localhost:8080
    assets/             Embedded icon files
  installer/
    install.ps1         Windows installer
    install.sh          Linux/macOS installer
```

## License

[MIT](LICENSE)
