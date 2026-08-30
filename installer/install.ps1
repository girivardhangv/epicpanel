# EpicPanel installer (Windows) - installs EpicPanel and its own PostgreSQL.
# Hosting software (Nginx, PHP, MariaDB, Redis, Node, Docker) is added later
# from the EpicPanel web UI. Run from an elevated PowerShell:
#
#   powershell -c "irm https://get.epichostly.in | iex"
#
$ErrorActionPreference = "Stop"

$Repo    = if ($env:EPICPANEL_REPO)    { $env:EPICPANEL_REPO }    else { "girivardhangv/epicpanel" }
$Version = if ($env:EPICPANEL_VERSION) { $env:EPICPANEL_VERSION } else { "latest" }
$Port    = if ($env:EPICPANEL_PORT)    { [int]$env:EPICPANEL_PORT } else { 8080 }
$InstallDir = "C:\Program Files\EpicPanel"
$DataDir    = "C:\ProgramData\EpicPanel"
$Bin        = Join-Path $InstallDir "epicpanel.exe"
$TaskName   = "EpicPanel"

function Ok($m)   { Write-Host "  [OK] $m" -ForegroundColor Green }
function Skip($m) { Write-Host "  [..] $m (already done)" -ForegroundColor DarkGray }
function Warn($m) { Write-Host "  [!] $m" -ForegroundColor Yellow }
function Fail($m) { Write-Host "  [X] $m" -ForegroundColor Red; exit 1 }
function Step($m) { Write-Host ""; Write-Host $m -ForegroundColor Cyan }

# ---- preconditions ---------------------------------------------------------
$IsAdmin = ([Security.Principal.WindowsPrincipal] `
  [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $IsAdmin) { Fail "Run from an elevated PowerShell (Administrator)." }

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
Ok "Architecture: $Arch"

$freeGB = [math]::Round(((Get-PSDrive C).Free / 1GB), 1)
if ($freeGB -lt 2) { Fail "Need at least 2 GB free disk on C: (have $freeGB GB)." }
Ok "Disk: $freeGB GB free"

# ---- PostgreSQL (EpicPanel's own core dependency) --------------------------
Step "PostgreSQL (EpicPanel's database)"
$pgService = Get-Service -Name "postgresql*" -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $pgService) {
  if (Get-Command winget -ErrorAction SilentlyContinue) {
    Warn "PostgreSQL not found - installing via winget (this may take a minute)."
    winget install -e --id PostgreSQL.PostgreSQL.16 --accept-package-agreements --accept-source-agreements --silent
    $pgService = Get-Service -Name "postgresql*" -ErrorAction SilentlyContinue | Select-Object -First 1
  }
}
if ($pgService) {
  if ($pgService.Status -ne "Running") { Start-Service $pgService.Name }
  Ok "PostgreSQL service: $($pgService.Name)"
} else {
  Warn "Could not install PostgreSQL automatically. Install it manually, then set EPICPANEL_DATABASE_DSN."
}

# Locate psql to provision the role/database.
$psql = $null
if ($pgService) {
  $pgBin = (Get-ItemProperty "HKLM:\SOFTWARE\PostgreSQL\Installations\*" -ErrorAction SilentlyContinue).BaseDirectory
  if ($pgBin) { $cand = Join-Path $pgBin "bin\psql.exe"; if (Test-Path $cand) { $psql = $cand } }
}
if (-not $psql) { $c = Get-Command psql -ErrorAction SilentlyContinue; if ($c) { $psql = $c.Source } }

$Dsn = $env:EPICPANEL_DATABASE_DSN
if (-not $Dsn -and $psql) {
  $pw = -join ((48..57)+(97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_})
  # Idempotent role + database creation (assumes local superuser access).
  $roleSql = 'DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname=''epicpanel'') THEN CREATE ROLE epicpanel LOGIN PASSWORD ''' + $pw + '''; END IF; END $$;'
  & $psql -U postgres -h 127.0.0.1 -c $roleSql | Out-Null
  & $psql -U postgres -h 127.0.0.1 -c "CREATE DATABASE epicpanel OWNER epicpanel" 2>$null | Out-Null
  $Dsn = "postgres://epicpanel:$pw@127.0.0.1:5432/epicpanel?sslmode=disable"
  Ok "database 'epicpanel' ready"
}
if (-not $Dsn) { $Dsn = "postgres://epicpanel:CHANGE_ME@127.0.0.1:5432/epicpanel?sslmode=disable"; Warn "Using placeholder DSN - edit it in the service environment." }

# ---- EpicPanel binary ------------------------------------------------------
Step "Downloading EpicPanel"
New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
if ($Version -eq "latest") { $base = "https://github.com/$Repo/releases/latest/download" }
else { $base = "https://github.com/$Repo/releases/download/$Version" }
$asset = "epicpanel_windows_$Arch.exe"
$tmp = Join-Path $env:TEMP "epicpanel-$asset"
try { Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing }
catch { Fail "Download failed: $base/$asset (set EPICPANEL_REPO/EPICPANEL_VERSION)." }
# Checksum (best effort).
try {
  $sums = Invoke-WebRequest -Uri "$base/checksums.txt" -UseBasicParsing -ErrorAction Stop
  $line = ($sums.Content -split "`n" | Where-Object { $_ -match [regex]::Escape($asset) } | Select-Object -First 1)
  if ($line) {
    $want = ($line -split '\s+')[0]
    $got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
    if ($want.ToLower() -ne $got) { Remove-Item $tmp; Fail "checksum mismatch for $asset" }
    Ok "checksum verified"
  }
} catch { Warn "checksums.txt unavailable; skipped verification" }
Move-Item -Force $tmp $Bin
Ok "installed $Bin"

# ---- environment + scheduled task -----------------------------------------
Step "Configuration and service"
[Environment]::SetEnvironmentVariable("EPICPANEL_DATABASE_DSN", $Dsn, "Machine")
[Environment]::SetEnvironmentVariable("EPICPANEL_DATA_DIR", $DataDir, "Machine")
[Environment]::SetEnvironmentVariable("EPICPANEL_SERVER_ADDR", ":$Port", "Machine")
[Environment]::SetEnvironmentVariable("EPICPANEL_SERVER_ENVIRONMENT", "production", "Machine")
Ok "machine environment configured"

schtasks /Query /TN $TaskName 2>$null | Out-Null
if ($LASTEXITCODE -eq 0) { Skip "scheduled task '$TaskName'" }
else {
  schtasks /Create /TN $TaskName /TR "`"$Bin`"" /SC ONSTART /RU SYSTEM /RL HIGHEST /F | Out-Null
  Ok "scheduled task '$TaskName' created (runs at startup)"
}

# Firewall
if (-not (Get-NetFirewallRule -DisplayName "EpicPanel" -ErrorAction SilentlyContinue)) {
  New-NetFirewallRule -DisplayName "EpicPanel" -Direction Inbound -LocalPort $Port -Action Allow | Out-Null
  Ok "firewall rule for port $Port"
} else { Skip "firewall rule" }

# ---- start + verify --------------------------------------------------------
Step "Starting EpicPanel"
schtasks /Run /TN $TaskName | Out-Null
$healthy = $false
for ($i = 0; $i -lt 20; $i++) {
  Start-Sleep -Seconds 1
  try { Invoke-WebRequest -Uri "http://127.0.0.1:$Port/healthz" -UseBasicParsing -TimeoutSec 2 | Out-Null; $healthy = $true; break } catch {}
}
if ($healthy) { Ok "health check passed" } else { Warn "EpicPanel not answering yet - check the scheduled task and re-open the URL." }

# ---- result ----------------------------------------------------------------
$ip = (Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notlike "127.*" -and $_.PrefixOrigin -ne "WellKnown" } | Select-Object -First 1).IPAddress
if (-not $ip) { $ip = "<server-ip>" }
Write-Host ""
Write-Host "EpicPanel installation complete!" -ForegroundColor Green
Write-Host "  Panel:   http://${ip}:$Port"
Write-Host "  Service: schtasks /Query /TN $TaskName"
Write-Host ""
Write-Host "Open the URL above to finish setup, then install hosting software from the panel."
