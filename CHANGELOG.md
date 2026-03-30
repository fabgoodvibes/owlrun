# Changelog

All notable changes to the Owlrun client are documented here.

## v0.9.19 — 2026-03-30

- Fix: wallet AutoClaim tests use msats not sats

## v0.9.18 — 2026-03-29

- OpenAI-format proxy support (compatible with any OpenAI API client)
- Ollama keep-warm control from dashboard
- Debug log gating (no more noisy logs in production)
- Free tier + karma UI in dashboard

## v0.9.17 — 2026-03-26

- Job mode toggle in dashboard (accept all / pause / selective)

## v0.9.16 — 2026-03-25

- CI: merge build + deploy into single job, remove artifact dependency

## v0.9.15 — 2026-03-25

- Fix: replace remaining Unicode box-drawing chars in install.ps1

## v0.9.14 — 2026-03-24

- Fix: purge non-ASCII from install.ps1 (crashed on Windows `irm | iex`)

## v0.9.13 — 2026-03-24

- Zero-config onboarding: auto-generate provider key on first run, show in dashboard

## v0.9.12 — 2026-03-24

- Add smollm2:135m, smollm2:360m, tinyllama:1.1b to model table

## v0.9.11 — 2026-03-24

- Fix: resolve 6 money bugs from security audit
- Harden wallet and earnings error handling

## v0.9.10 — 2026-03-23

- Fix: detect already-running Ollama before trying to start it

## v0.9.9 — 2026-03-23

- Fix: show actionable error messages when Ollama is missing or no models are installed

## v0.9.8 — 2026-03-23

- Earnings card shows sats as hero number
- Fix: JS crash from var/const conflict in dashboard

## v0.9.7 — 2026-03-23

- Line charts with cumulative sum + smart USD scale for micro earnings

## v0.9.6 — 2026-03-23

- Model table update with new entries
- Dashboard spinners and AVAILABLE badges
- Payout history view

## v0.9.5 — 2026-03-22

- Dashboard model manager with install/remove
- Dark/light theme toggle
- Earnings precision improvements

## v0.9.4 — 2026-03-22

- Model picker in dashboard with per-model pricing display

## v0.9.3 — 2026-03-22

- Multi-model registration: register all installed models with gateway

## v0.9.2 — 2026-03-22

- WebSocket proxy: stream Ollama response over WS instead of HTTP POST

## v0.9.1 — 2026-03-21

- First beta release
- BTC-native payments: Lightning address auto-payout, ecash withdrawal
- Millisat internal accounting for sub-sat precision
- Model pricing display synced with gateway
- Dashboard: wallet setup, earnings charts, broadcast notifications
- WS proxy for streaming inference
- Multi-model registration

## v0.2.0-beta — 2026-03-15

- Beta build system with beta/production network split
- 5-state system tray (connected, earning, idle, error, offline)
- Wallet setup nudge in dashboard
- Cloudflare Pages CDN for binary distribution
- SHA-256 checksum verification in installers

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
