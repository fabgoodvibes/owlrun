#Requires -Version 5.1
<#
.SYNOPSIS
  Owlrun Windows installer.
  Usage: irm https://get.owlrun.me/install.ps1 -OutFile install.ps1; .\install.ps1 -ApiKey owlr_prov_...
  Or:    irm https://get.owlrun.me/install.ps1 | iex
.DESCRIPTION
  - Detects GPU (NVIDIA / AMD)
  - Checks disk space
  - Downloads and installs Ollama (silent) if absent
  - Downloads owlrun.exe from get.owlrun.me CDN
  - Writes default config to ~/.owlrun/owlrun.conf
  - Registers a Task Scheduler logon task (no admin required)
  - Launches owlrun immediately
#>

param(
  [string]$ApiKey = '',
  [string]$Wallet = '',
  [string]$Referral = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ── Constants ────────────────────────────────────────────────────────────────

$INSTALL_DIR  = Join-Path $env:LOCALAPPDATA 'Owlrun'
$CONFIG_DIR   = Join-Path $env:USERPROFILE  '.owlrun'
$CONFIG_FILE  = Join-Path $CONFIG_DIR       'owlrun.conf'
$EXE_PATH     = Join-Path $INSTALL_DIR      'owlrun.exe'
$TASK_NAME    = 'Owlrun'
$DOWNLOAD_URL    = 'https://get.owlrun.me/download/beta/latest/owlrun-windows-amd64.exe'
$CHECKSUMS_URL   = 'https://get.owlrun.me/download/beta/latest/checksums.txt'
$OLLAMA_URL   = 'https://ollama.com/download/OllamaSetup.exe'
$OLLAMA_EXE   = Join-Path $env:LOCALAPPDATA 'Programs\Ollama\ollama.exe'
$MIN_DISK_GB  = 8
$WARN_DISK_PCT = 30

# ── Helpers ──────────────────────────────────────────────────────────────────

function Write-Step  { param($msg) Write-Host "  → $msg" -ForegroundColor Cyan }
function Write-OK    { param($msg) Write-Host "  ✓ $msg" -ForegroundColor Green }
function Write-Warn  { param($msg) Write-Host "  ⚠ $msg" -ForegroundColor Yellow }
function Write-Fail  { param($msg) Write-Host "  ✗ $msg" -ForegroundColor Red }
function Write-Title { param($msg) Write-Host "`n$msg" -ForegroundColor White }

function Get-DiskInfo {
  param([string]$Path)
  $drive = Split-Path -Qualifier $Path
  $disk  = Get-PSDrive -Name ($drive.TrimEnd(':')) -ErrorAction SilentlyContinue
  if (-not $disk) { return $null }
  $freeGB  = [math]::Round($disk.Free  / 1GB, 1)
  $totalGB = [math]::Round(($disk.Free + $disk.Used) / 1GB, 1)
  $freePct = if ($totalGB -gt 0) { [math]::Round($freeGB / $totalGB * 100, 0) } else { 0 }
  return @{ FreeGB = $freeGB; TotalGB = $totalGB; FreePct = $freePct }
}

function Download-File {
  param([string]$Url, [string]$Dest, [string]$Label)
  Write-Step "Downloading $Label…"
  $tmp = "$Dest.tmp"
  try {
    $wc = New-Object System.Net.WebClient
    $wc.Headers.Add('User-Agent', "owlrun-installer/1.0")
    $wc.DownloadFile($Url, $tmp)
    Move-Item -Force $tmp $Dest
    Write-OK "$Label downloaded"
  } catch {
    if (Test-Path $tmp) { Remove-Item $tmp -Force }
    throw "Download failed: $_"
  }
}

function Get-GpuInfo {
  # Try NVIDIA first via nvidia-smi
  $nvidiaSmi = Get-Command 'nvidia-smi' -ErrorAction SilentlyContinue
  if ($nvidiaSmi) {
    try {
      $out = & nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>$null
      if ($out) {
        $parts = $out.Trim().Split(',')
        $name  = $parts[0].Trim()
        $vramMB = [int]($parts[1].Trim() -replace ' MiB','')
        return @{ Vendor = 'nvidia'; Name = $name; VRAMTotalMB = $vramMB }
      }
    } catch {}
  }

  # AMD/other via WMI
  try {
    $gpu = Get-WmiObject Win32_VideoController |
           Where-Object { $_.AdapterRAM -gt 0 } |
           Sort-Object AdapterRAM -Descending |
           Select-Object -First 1
    if ($gpu) {
      $vramMB = [math]::Round($gpu.AdapterRAM / 1MB)
      $vendor = if ($gpu.Name -match 'AMD|Radeon') { 'amd' }
                elseif ($gpu.Name -match 'NVIDIA')  { 'nvidia' }
                else                                { 'other' }
      return @{ Vendor = $vendor; Name = $gpu.Name; VRAMTotalMB = $vramMB }
    }
  } catch {}

  return @{ Vendor = 'none'; Name = 'Unknown'; VRAMTotalMB = 0 }
}

function Install-Ollama {
  if (Test-Path $OLLAMA_EXE) {
    Write-OK "Ollama already installed"
    return
  }
  $setup = Join-Path $env:TEMP 'OllamaSetup.exe'
  Download-File $OLLAMA_URL $setup 'Ollama'
  Write-Step "Installing Ollama (silent)…"
  Start-Process -FilePath $setup -ArgumentList '/S' -Wait
  Remove-Item $setup -Force -ErrorAction SilentlyContinue
  if (Test-Path $OLLAMA_EXE) {
    Write-OK "Ollama installed"
  } else {
    Write-Warn "Ollama installer ran but ollama.exe not found at default path — continuing anyway"
  }
}

function Register-StartupTask {
  # Register a Task Scheduler task that runs owlrun at every user logon.
  # Uses the current user's account — no admin required.
  $existing = Get-ScheduledTask -TaskName $TASK_NAME -ErrorAction SilentlyContinue
  if ($existing) {
    Write-OK "Startup task already registered"
    return
  }
  $action  = New-ScheduledTaskAction -Execute $EXE_PATH
  $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
  $settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit (New-TimeSpan -Hours 0) `
    -MultipleInstances IgnoreNew `
    -StartWhenAvailable
  $principal = New-ScheduledTaskPrincipal `
    -UserId $env:USERNAME `
    -LogonType Interactive `
    -RunLevel Limited

  Register-ScheduledTask `
    -TaskName  $TASK_NAME `
    -Action    $action `
    -Trigger   $trigger `
    -Settings  $settings `
    -Principal $principal `
    -Description "Owlrun — idle GPU earning agent" | Out-Null

  Write-OK "Startup task registered (runs at logon)"
}

function Write-DefaultConfig {
  param([string]$NodeId, [string]$ApiKey, [string]$Wallet, [string]$ReferralCode)
  $conf = @"
; Owlrun configuration — ~/.owlrun/owlrun.conf
; Edit this file to customise your node.

[account]
node_id       = $NodeId
api_key       = $ApiKey
wallet        = $Wallet
referral_code = $ReferralCode

[marketplace]
gateway        = https://node.owlrun.me
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
"@
  New-Item -ItemType Directory -Force -Path $CONFIG_DIR | Out-Null
  Set-Content -Path $CONFIG_FILE -Value $conf -Encoding UTF8
  Write-OK "Config written to $CONFIG_FILE"
}

# ── Main ─────────────────────────────────────────────────────────────────────

Clear-Host
Write-Host ""
Write-Host "  ████████╗ Owlrun Installer" -ForegroundColor Green
Write-Host "  ╚══════╝  Earn money while your GPU sleeps" -ForegroundColor DarkGreen
Write-Host ""

# ── 1. GPU detection ─────────────────────────────────────────────────────────
Write-Title "[ 1/7 ] Detecting GPU"
$gpu = Get-GpuInfo
if ($gpu.Vendor -eq 'none') {
  Write-Warn "No GPU detected — will run CPU-only (small models, lower earnings)"
} else {
  $vramGB = [math]::Round($gpu.VRAMTotalMB / 1024, 1)
  Write-OK "$($gpu.Name) — $vramGB GB VRAM ($($gpu.Vendor.ToUpper()))"
}

# ── 2. Disk space check ───────────────────────────────────────────────────────
Write-Title "[ 2/7 ] Checking disk space"
$diskDrive = Split-Path -Qualifier $env:USERPROFILE
$disk = Get-DiskInfo $diskDrive
if ($disk) {
  if ($disk.FreeGB -lt $MIN_DISK_GB) {
    Write-Fail "Only $($disk.FreeGB) GB free on $diskDrive — need at least $MIN_DISK_GB GB for AI models."
    Write-Host "        Please free up disk space and re-run the installer." -ForegroundColor DarkGray
    Read-Host "Press Enter to exit"
    exit 1
  } elseif ($disk.FreePct -lt $WARN_DISK_PCT) {
    Write-Warn "$($disk.FreeGB) GB free ($($disk.FreePct)%) — recommended: >$WARN_DISK_PCT%"
    Write-Warn "Model downloads may eventually fail. Consider freeing space."
  } else {
    Write-OK "$($disk.FreeGB) GB free ($($disk.FreePct)%) on $diskDrive"
  }
} else {
  Write-Warn "Could not check disk space — continuing"
}

# ── 3. Ollama ─────────────────────────────────────────────────────────────────
Write-Title "[ 3/7 ] Checking Ollama"
Install-Ollama

# ── 4. Download owlrun.exe ────────────────────────────────────────────────────
Write-Title "[ 4/7 ] Installing Owlrun"
New-Item -ItemType Directory -Force -Path $INSTALL_DIR | Out-Null

if (Test-Path $EXE_PATH) {
  Write-OK "owlrun.exe already present — updating"
}
Download-File $DOWNLOAD_URL $EXE_PATH 'owlrun.exe'

# Verify SHA-256 checksum
Write-Step "Verifying integrity…"
try {
  $checksumsTmp = Join-Path $env:TEMP 'owlrun-checksums.txt'
  $wc = New-Object System.Net.WebClient
  $wc.Headers.Add('User-Agent', "owlrun-installer/1.0")
  $wc.DownloadFile($CHECKSUMS_URL, $checksumsTmp)
  $lines = Get-Content $checksumsTmp
  $entry = $lines | Where-Object { $_ -match 'owlrun-windows-amd64\.exe$' } | Select-Object -First 1
  if ($entry) {
    $expected = ($entry -split '\s+')[0]
    $actual = (Get-FileHash -Path $EXE_PATH -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) {
      Remove-Item $EXE_PATH -Force -ErrorAction SilentlyContinue
      Remove-Item $checksumsTmp -Force -ErrorAction SilentlyContinue
      Write-Fail "Checksum mismatch! Expected $expected, got $actual"
      Write-Fail "The download may be corrupted or tampered with. Aborting."
      Read-Host "Press Enter to exit"
      exit 1
    }
    Write-OK "Checksum verified (SHA-256)"
  } else {
    Write-Warn "owlrun-windows-amd64.exe not found in checksums.txt — skipping verification"
  }
  Remove-Item $checksumsTmp -Force -ErrorAction SilentlyContinue
} catch {
  Write-Warn "Could not fetch checksums.txt — skipping verification"
}

# ── 5. Config wizard ──────────────────────────────────────────────────────────
Write-Title "[ 5/7 ] Configuration"

$existingNodeId = $null
if (Test-Path $CONFIG_FILE) {
  # Read existing node_id to preserve it across re-installs.
  $existingNodeId = (Select-String -Path $CONFIG_FILE -Pattern '^node_id\s*=\s*(.+)$').Matches.Groups[1].Value.Trim()
}
$nodeId = if ($existingNodeId) { $existingNodeId } else { [System.Guid]::NewGuid().ToString() }

$apiKey = ''
$wallet = ''

if (-not (Test-Path $CONFIG_FILE)) {
  # Auto-generate provider key — no user input needed.
  # The binary also auto-generates on first run, but we do it here too
  # so the config file is complete from the start.
  if ($ApiKey) {
    $apiKey = $ApiKey.Trim()
  } else {
    $bytes = New-Object byte[] 24
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $apiKey = "owlr_prov_" + [BitConverter]::ToString($bytes).Replace('-','').ToLower()
  }
  Write-OK "Provider key: $apiKey"

  $wallet   = $Wallet.Trim()
  $referral = $Referral.Trim()
  Write-DefaultConfig -NodeId $nodeId -ApiKey $apiKey -Wallet $wallet -ReferralCode $referral
} else {
  Write-OK "Existing config preserved at $CONFIG_FILE"
}

# ── 6. Startup task ───────────────────────────────────────────────────────────
Write-Title "[ 6/7 ] Registering startup task"
Register-StartupTask

# ── 7. Launch ─────────────────────────────────────────────────────────────────
Write-Title "[ 7/7 ] Launching Owlrun"
Start-Process -FilePath $EXE_PATH
Write-OK "Owlrun is running — look for the owl icon in your system tray"

# ── Done ──────────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host "  ╔══════════════════════════════════════════╗" -ForegroundColor Green
Write-Host "  ║  🦉 Owlrun installed successfully!        ║" -ForegroundColor Green
Write-Host "  ║                                           ║" -ForegroundColor Green
Write-Host "  ║  Dashboard → http://localhost:19131        ║" -ForegroundColor Green
Write-Host "  ║  Config    → $($CONFIG_FILE.PadRight(33)) ║" -ForegroundColor Green
Write-Host "  ╚══════════════════════════════════════════╝" -ForegroundColor Green
Write-Host ""
