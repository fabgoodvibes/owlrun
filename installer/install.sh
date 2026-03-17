#!/usr/bin/env bash
# Owlrun installer — Linux & macOS
#
# Linux:  curl -fsSL https://get.owlrun.me/install.sh | bash
# macOS:  curl -fsSL https://get.owlrun.me/install.sh | bash
#
# - Detects OS and GPU
# - Checks disk space
# - Installs Ollama if absent
# - Downloads owlrun binary to ~/.local/bin/owlrun
# - Writes default config to ~/.owlrun/owlrun.conf
# - Registers auto-start (systemd user service on Linux, launchd on macOS)
# - Launches owlrun immediately

set -euo pipefail

# ── Constants ─────────────────────────────────────────────────────────────────

DOWNLOAD_BASE="https://get.owlrun.me/download/beta/latest"
INSTALL_DIR="$HOME/.local/bin"
EXE_PATH="$INSTALL_DIR/owlrun"
CONFIG_DIR="$HOME/.owlrun"
CONFIG_FILE="$CONFIG_DIR/owlrun.conf"
MIN_DISK_GB=8
WARN_DISK_PCT=30

# ── CLI args ──────────────────────────────────────────────────────────────────

CLI_KEY=""
CLI_WALLET=""
CLI_REFERRAL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --key)      CLI_KEY="$2"; shift 2 ;;
    --wallet)   CLI_WALLET="$2"; shift 2 ;;
    --referral) CLI_REFERRAL="$2"; shift 2 ;;
    *) shift ;;
  esac
done

# ── Colours ───────────────────────────────────────────────────────────────────

RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

step()  { echo -e "  ${CYAN}→${RESET} $*"; }
ok()    { echo -e "  ${GREEN}✓${RESET} $*"; }
warn()  { echo -e "  ${YELLOW}⚠${RESET} $*"; }
fail()  { echo -e "  ${RED}✗${RESET} $*" >&2; }
title() { echo -e "\n${BOLD}$*${RESET}"; }

# ── Helpers ───────────────────────────────────────────────────────────────────

need_cmd() {
  if ! command -v "$1" &>/dev/null; then
    fail "Required command not found: $1"; exit 1
  fi
}

detect_os() {
  case "$(uname -s)" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)      fail "Unsupported OS: $(uname -s)"; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64)        echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)             fail "Unsupported architecture: $(uname -m)"; exit 1 ;;
  esac
}

detect_gpu() {
  GPU_VENDOR="none"; GPU_NAME="none"; GPU_VRAM_MB=0

  # NVIDIA (Linux + macOS)
  if command -v nvidia-smi &>/dev/null; then
    local info
    info=$(nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>/dev/null | head -1 || true)
    if [[ -n "$info" ]]; then
      GPU_VENDOR="nvidia"
      GPU_NAME=$(echo "$info" | cut -d',' -f1 | xargs)
      GPU_VRAM_MB=$(echo "$info" | cut -d',' -f2 | tr -dc '0-9')
      return
    fi
  fi

  # Apple Silicon
  if [[ "$OS" == "darwin" ]] && [[ "$(uname -m)" == "arm64" ]]; then
    GPU_VENDOR="apple"
    GPU_NAME=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "Apple Silicon")
    GPU_VRAM_MB=$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1024 / 1024 ))
    return
  fi

  # AMD on Linux
  if [[ "$OS" == "linux" ]] && command -v rocm-smi &>/dev/null; then
    GPU_VENDOR="amd"; GPU_NAME="AMD GPU (ROCm)"; GPU_VRAM_MB=0
  fi
}

check_disk() {
  local avail_kb total_kb avail_gb free_pct avail_int
  avail_kb=$(df -k "$HOME" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)
  total_kb=$(df -k "$HOME" 2>/dev/null | awk 'NR==2{print $2}' || echo 1)
  avail_gb=$(awk "BEGIN{printf \"%.1f\", $avail_kb/1048576}")
  free_pct=$(( avail_kb * 100 / total_kb ))
  avail_int=${avail_gb%.*}
  if (( avail_int < MIN_DISK_GB )); then
    fail "Only ${avail_gb} GB free — need at least ${MIN_DISK_GB} GB for AI models."; exit 1
  elif (( free_pct < WARN_DISK_PCT )); then
    warn "${avail_gb} GB free (${free_pct}%) — model downloads may eventually fail"
  else
    ok "${avail_gb} GB free (${free_pct}%)"
  fi
}

install_ollama() {
  if command -v ollama &>/dev/null; then
    ok "Ollama already installed"; return
  fi
  step "Installing Ollama…"
  curl -fsSL https://ollama.com/install.sh | sh
  ok "Ollama installed"
}

download_owlrun() {
  local arch="$1"
  local binary_name="owlrun-${OS}-${arch}"
  step "Downloading owlrun (${OS}/${arch})…"
  mkdir -p "$INSTALL_DIR"
  curl -fsSL --output "$EXE_PATH.tmp" "${DOWNLOAD_BASE}/${binary_name}"

  # Verify SHA-256 checksum
  step "Verifying integrity…"
  local checksums_file
  checksums_file=$(mktemp)
  if curl -fsSL --output "$checksums_file" "${DOWNLOAD_BASE}/checksums.txt" 2>/dev/null; then
    local expected actual
    expected=$(grep "${binary_name}$" "$checksums_file" | awk '{print $1}')
    if [[ -n "$expected" ]]; then
      if command -v sha256sum &>/dev/null; then
        actual=$(sha256sum "$EXE_PATH.tmp" | awk '{print $1}')
      elif command -v shasum &>/dev/null; then
        actual=$(shasum -a 256 "$EXE_PATH.tmp" | awk '{print $1}')
      else
        warn "No sha256sum or shasum found — skipping checksum verification"
        actual="$expected"
      fi
      if [[ "$actual" != "$expected" ]]; then
        rm -f "$EXE_PATH.tmp" "$checksums_file"
        fail "Checksum mismatch! Expected ${expected}, got ${actual}"
        fail "The download may be corrupted or tampered with. Aborting."
        exit 1
      fi
      ok "Checksum verified (SHA-256)"
    else
      warn "Binary not found in checksums.txt — skipping verification"
    fi
  else
    warn "Could not fetch checksums.txt — skipping verification"
  fi
  rm -f "$checksums_file"

  chmod +x "$EXE_PATH.tmp"
  mv "$EXE_PATH.tmp" "$EXE_PATH"
  ok "owlrun installed to $EXE_PATH"
}

write_config() {
  local node_id="$1" api_key="$2" wallet="$3" referral="$4"
  mkdir -p "$CONFIG_DIR"
  cat > "$CONFIG_FILE" <<EOF
; Owlrun configuration — ~/.owlrun/owlrun.conf

[account]
node_id       = $node_id
api_key       = $api_key
wallet        = $wallet
referral_code = $referral

[marketplace]
gateway        = https://gateway.owlrun.me
allow_override = true

[inference]
model_auto   = true
max_vram_pct = 80

[idle]
trigger_minutes = 10
gpu_threshold   = 15
watch_processes = true

[disk]
warn_threshold_pct = 30
min_model_space_gb = 8
EOF
  ok "Config written to $CONFIG_FILE"
}

register_autostart_linux() {
  # Prefer systemd user service; fall back to XDG autostart desktop entry.
  if command -v systemctl &>/dev/null && systemctl --user status &>/dev/null 2>&1; then
    local svc_dir="$HOME/.config/systemd/user"
    mkdir -p "$svc_dir"
    cat > "$svc_dir/owlrun.service" <<EOF
[Unit]
Description=Owlrun — idle GPU earning agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$EXE_PATH
Restart=on-failure
RestartSec=10
StandardOutput=append:%h/.owlrun/owlrun.log
StandardError=append:%h/.owlrun/owlrun.log

[Install]
WantedBy=default.target
EOF
    systemctl --user daemon-reload
    systemctl --user enable owlrun
    ok "systemd user service registered (auto-starts at login)"
  else
    # XDG autostart fallback — works on GNOME, KDE, XFCE
    local autostart_dir="$HOME/.config/autostart"
    mkdir -p "$autostart_dir"
    cat > "$autostart_dir/owlrun.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Owlrun
Exec=$EXE_PATH
Hidden=false
NoDisplay=false
X-GNOME-Autostart-enabled=true
Comment=Owlrun — idle GPU earning agent
EOF
    ok "XDG autostart entry registered (auto-starts at login)"
  fi
}

register_autostart_macos() {
  local label="me.owlrun.agent"
  local plist="$HOME/Library/LaunchAgents/$label.plist"
  mkdir -p "$(dirname "$plist")"
  cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>$label</string>
  <key>ProgramArguments</key>  <array><string>$EXE_PATH</string></array>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>         <false/>
  <key>StandardOutPath</key>   <string>$HOME/.owlrun/owlrun.log</string>
  <key>StandardErrorPath</key> <string>$HOME/.owlrun/owlrun.log</string>
</dict>
</plist>
EOF
  launchctl load -w "$plist" 2>/dev/null || true
  ok "launchd agent registered (auto-starts at login)"
}

gen_uuid() {
  if command -v uuidgen &>/dev/null; then
    uuidgen | tr '[:upper:]' '[:lower:]'
  elif [[ -f /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
  else
    od -x /dev/urandom | head -1 | awk '{print $2$3"-"$4"-"$5"-"$6"-"$7$8$9}'
  fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}  ████████╗ Owlrun Installer${RESET}"
echo -e "${GREEN}  ╚══════╝  Earn money while your GPU sleeps${RESET}"
echo ""

echo -e "  This installer will:"
echo -e "    • Detect your GPU and disk space"
echo -e "    • Install Ollama (if not present) — ${BOLD}requires sudo${RESET}"
echo -e "    • Download the Owlrun binary to ~/.local/bin/"
echo -e "    • Write config to ~/.owlrun/owlrun.conf"
echo -e "    • Register auto-start (systemd/launchd)"
echo ""
read -rp "  Continue? [y/N] " CONFIRM
if [[ ! "$CONFIRM" =~ ^[Yy]$ ]]; then
  echo "  Aborted."; exit 0
fi
echo ""

# Pre-authorize sudo so it doesn't interrupt mid-install.
if ! sudo -v 2>/dev/null; then
  fail "sudo is required to install Ollama. Please run as a user with sudo access."
  exit 1
fi

need_cmd curl

# ── 1. Hardware detection ────────────────────────────────────────────────────
title "[ 1/7 ] Detecting hardware"
detect_os
detect_gpu

if [[ "$GPU_VENDOR" == "none" ]]; then
  if [[ "$OS" == "darwin" ]]; then
    fail "No supported GPU detected. Owlrun requires an NVIDIA GPU or Apple Silicon."; exit 1
  else
    warn "No GPU detected — will run CPU-only (small models, lower earnings)"
  fi
else
  GPU_VRAM_GB=$(awk "BEGIN{printf \"%.1f\", $GPU_VRAM_MB/1024}")
  ok "$GPU_NAME — ${GPU_VRAM_GB} GB VRAM ($GPU_VENDOR)"
fi

# ── 2. Disk space check ───────────────────────────────────────────────────────
title "[ 2/7 ] Checking disk space"
check_disk

# ── 3. Ollama ─────────────────────────────────────────────────────────────────
title "[ 3/7 ] Checking Ollama"
install_ollama

# ── 4. Download owlrun ────────────────────────────────────────────────────────
title "[ 4/7 ] Installing Owlrun"
ARCH=$(detect_arch)
download_owlrun "$ARCH"

if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  warn "$INSTALL_DIR is not in your PATH"
  warn "Add to your shell profile: export PATH=\"\$PATH:$INSTALL_DIR\""
fi

# ── 5. Config wizard ──────────────────────────────────────────────────────────
title "[ 5/7 ] Configuration"

NODE_ID=""
if [[ -f "$CONFIG_FILE" ]]; then
  NODE_ID=$(grep -E '^node_id\s*=' "$CONFIG_FILE" 2>/dev/null | cut -d'=' -f2 | xargs || true)
fi
[[ -z "$NODE_ID" ]] && NODE_ID=$(gen_uuid)

if [[ ! -f "$CONFIG_FILE" ]]; then
  if [[ -n "$CLI_KEY" ]]; then
    # Key provided via CLI — skip interactive prompts
    ok "API key provided via --key"
    write_config "$NODE_ID" "$CLI_KEY" "${CLI_WALLET:-}" "${CLI_REFERRAL:-}"
  else
    echo ""
    echo -e "  ${CYAN}Get your provider key at https://owlrun.me — or skip and add it later.${RESET}"
    echo -e "  Config file: $CONFIG_FILE"
    echo ""
    read -rp "  Provider key (press Enter to skip): " API_KEY
    read -rp "  Solana wallet for payouts (press Enter to skip): " WALLET
    read -rp "  Referral code (press Enter to skip): " REFERRAL
    write_config "$NODE_ID" "${API_KEY:-}" "${WALLET:-}" "${REFERRAL:-}"
  fi
else
  ok "Existing config preserved"
fi

# ── 6. Auto-start ─────────────────────────────────────────────────────────────
title "[ 6/7 ] Registering startup agent"
if [[ "$OS" == "linux" ]]; then
  register_autostart_linux
else
  register_autostart_macos
fi

# ── 7. Launch ─────────────────────────────────────────────────────────────────
title "[ 7/7 ] Launching Owlrun"
"$EXE_PATH" &
ok "Owlrun launched (PID $!)"

# ── Done ──────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}  ╔══════════════════════════════════════════╗${RESET}"
echo -e "${GREEN}  ║  Owlrun installed successfully!           ║${RESET}"
echo -e "${GREEN}  ║                                           ║${RESET}"
echo -e "${GREEN}  ║  Dashboard → http://localhost:19131        ║${RESET}"
echo -e "${GREEN}  ║  Logs      → ~/.owlrun/owlrun.log         ║${RESET}"
echo -e "${GREEN}  ║  Config    → ~/.owlrun/owlrun.conf        ║${RESET}"
echo -e "${GREEN}  ╚══════════════════════════════════════════╝${RESET}"
echo ""
