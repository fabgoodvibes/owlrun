# Changelog

## v0.1.0 — Initial release

- System tray agent for Windows, macOS, and Linux
- Idle detection with configurable timeout and game process scanning
- GPU detection: NVIDIA (nvidia-smi), AMD (WMI/ROCm), Apple Silicon
- Disk space guard with configurable thresholds
- Ollama lifecycle management (auto-install, start, pull, stop)
- Earnings tracker with SQLite (daily + all-time)
- Gateway connector with WebSocket control channel and HTTP/2 job proxy
- Local dashboard at localhost:19131
- One-line installers for Windows (PowerShell) and Unix (bash)
- Auto-start registration (Task Scheduler / systemd / launchd)
- CI/CD with GitHub Actions — builds all platforms, deploys to CDN
