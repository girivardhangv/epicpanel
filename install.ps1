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
function Die($m)  { throw $m }
function Step($m) { Write-Host ""; Write-Host $m -ForegroundColor Cyan }

function New-RandomPassword {
  -join ((48..57)+(65..90)+(97..122) | Get-Random -Count 24 | ForEach-Object { [char]$_ })
}

# Locate psql.exe from the running service, common install dirs, or PATH.
function Find-Psql {
  $svc = Get-CimInstance Win32_Service -Filter "Name LIKE 'postgresql%'" -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($svc -and $svc.PathName) {
    $exe = ($svc.PathName -replace '"','') -replace '\s+-.*$',''
    $cand = Join-Path (Split-Path $exe -Parent) 'psql.exe'
    if (Test-Path $cand) { return $cand }
  }
  $g = Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\psql.exe' -ErrorAction SilentlyContinue | Sort-Object FullName -Descending | Select-Object -First 1
  if ($g) { return $g.FullName }
  $c = Get-Command psql -ErrorAction SilentlyContinue
  if ($c) { return $c.Source }
  return $null
}

function Invoke-Psql($psql, $superPw, $sql) {
  $env:PGPASSWORD = $superPw
  try {
    $out = & $psql -U postgres -h 127.0.0.1 -v ON_ERROR_STOP=1 -c $sql 2>&1
    $code = $LASTEXITCODE
  } finally { Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue }
  return @{ Code = $code; Out = ($out | Out-String) }
}

function Install-EpicPanel {
  # ---- preconditions ----
  $IsAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
  if (-not $IsAdmin) { Die "Run from an elevated PowerShell (Administrator)." }

  $Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
  Ok "Architecture: $Arch"

  $freeGB = [math]::Round(((Get-PSDrive C).Free / 1GB), 1)
  if ($freeGB -lt 2) { Die "Need at least 2 GB free disk on C: (have $freeGB GB)." }
  Ok "Disk: $freeGB GB free"

  # ---- PostgreSQL (EpicPanel's own core dependency) ----
  Step "PostgreSQL (EpicPanel's database)"
  $pgService = Get-Service -Name "postgresql*" -ErrorAction SilentlyContinue | Select-Object -First 1
  if (-not $pgService) {
    if (Get-Command winget -ErrorAction SilentlyContinue) {
      Warn "PostgreSQL not found - installing via winget (this can take a few minutes)."
      winget install -e --id PostgreSQL.PostgreSQL.16 --accept-package-agreements --accept-source-agreements --silent
      $pgService = Get-Service -Name "postgresql*" -ErrorAction SilentlyContinue | Select-Object -First 1
    }
  }
  if ($pgService) {
    if ($pgService.Status -ne "Running") { Start-Service $pgService.Name }
    Ok "PostgreSQL service: $($pgService.Name)"
  } else {
    Warn "PostgreSQL not found and could not be installed automatically."
  }

  # ---- provision EpicPanel's database ----
  $Dsn = $env:EPICPANEL_DATABASE_DSN
  if (-not $Dsn) {
    $psql = Find-Psql
    if ($psql) {
      Ok "found psql: $psql"
      $epPw = New-RandomPassword
      $superPw = $env:EPICPANEL_PG_SUPERPASSWORD
      if (-not $superPw) {
        $sec = Read-Host "Enter the PostgreSQL 'postgres' superuser password (blank to skip auto-setup)" -AsSecureString
        if ($sec.Length -gt 0) {
          $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
          try { $superPw = [Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr) }
          finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
        }
      }
      if ($superPw) {
        $roleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='epicpanel') THEN CREATE ROLE epicpanel LOGIN PASSWORD '$epPw'; ELSE ALTER ROLE epicpanel LOGIN PASSWORD '$epPw'; END IF; END `$`$;"
        $r1 = Invoke-Psql $psql $superPw $roleSql
        if ($r1.Code -eq 0) {
          $exists = Invoke-Psql $psql $superPw "SELECT 1 FROM pg_database WHERE datname='epicpanel'"
          if ($exists.Out -notmatch '1') {
            Invoke-Psql $psql $superPw "CREATE DATABASE epicpanel OWNER epicpanel" | Out-Null
          }
          $Dsn = "postgres://epicpanel:$epPw@127.0.0.1:5432/epicpanel?sslmode=disable"
          Ok "database 'epicpanel' ready"
        } else {
          Warn "Could not create the role/database automatically. Error: $($r1.Out.Trim())"
        }
      } else {
        Warn "Skipped database auto-setup (no superuser password provided)."
      }
    } else {
      Warn "psql.exe not found; configure a DSN manually."
    }
  }
  if (-not $Dsn) {
    Warn "No DSN provisioned. EpicPanel will start but you must set EPICPANEL_DATABASE_DSN and restart the task."
    $Dsn = "postgres://epicpanel:CHANGE_ME@127.0.0.1:5432/epicpanel?sslmode=disable"
  }

  # ---- download EpicPanel ----
  Step "Downloading EpicPanel"
  New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
  if ($Version -eq "latest") { $base = "https://github.com/$Repo/releases/latest/download" }
  else { $base = "https://github.com/$Repo/releases/download/$Version" }
  $asset = "epicpanel_windows_$Arch.exe"
  $tmp = Join-Path $env:TEMP $asset
  try { Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing }
  catch { Die "Download failed: $base/$asset (is a GitHub Release published? set EPICPANEL_REPO/EPICPANEL_VERSION)." }
  try {
    $sums = (Invoke-WebRequest -Uri "$base/checksums.txt" -UseBasicParsing -ErrorAction Stop).Content
    $line = ($sums -split "`n" | Where-Object { $_ -match [regex]::Escape($asset) } | Select-Object -First 1)
    if ($line) {
      $want = ($line -split '\s+')[0].ToLower()
      $got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
      if ($want -ne $got) { Remove-Item $tmp -Force; Die "checksum mismatch for $asset" }
      Ok "checksum verified"
    }
  } catch { Warn "checksums.txt unavailable; skipped verification" }
  Move-Item -Force $tmp $Bin
  Ok "installed $Bin"

  # ---- environment + scheduled task ----
  Step "Configuration and service"
  [Environment]::SetEnvironmentVariable("EPICPANEL_DATABASE_DSN", $Dsn, "Machine")
  [Environment]::SetEnvironmentVariable("EPICPANEL_DATA_DIR", $DataDir, "Machine")
  [Environment]::SetEnvironmentVariable("EPICPANEL_SERVER_ADDR", ":$Port", "Machine")
  [Environment]::SetEnvironmentVariable("EPICPANEL_SERVER_ENVIRONMENT", "production", "Machine")
  Ok "machine environment configured"

  if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Skip "scheduled task '$TaskName'"
  } else {
    $action   = New-ScheduledTaskAction -Execute $Bin -WorkingDirectory $InstallDir
    $trigger  = New-ScheduledTaskTrigger -AtStartup
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
                -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings `
                -User "SYSTEM" -RunLevel Highest -Force | Out-Null
    Ok "scheduled task '$TaskName' created (runs at startup)"
  }

  if (-not (Get-NetFirewallRule -DisplayName "EpicPanel" -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -DisplayName "EpicPanel" -Direction Inbound -LocalPort $Port -Action Allow | Out-Null
    Ok "firewall rule for port $Port"
  } else { Skip "firewall rule" }

  # ---- start + verify ----
  Step "Starting EpicPanel"
  Start-ScheduledTask -TaskName $TaskName
  $healthy = $false
  for ($i = 0; $i -lt 20; $i++) {
    Start-Sleep -Seconds 1
    try { Invoke-WebRequest -Uri "http://127.0.0.1:$Port/healthz" -UseBasicParsing -TimeoutSec 2 | Out-Null; $healthy = $true; break } catch {}
  }
  if ($healthy) { Ok "health check passed" } else { Warn "EpicPanel not answering yet - check the scheduled task, then reload the URL." }

  # ---- result ----
  $ip = (Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notlike "127.*" -and $_.PrefixOrigin -ne "WellKnown" } | Select-Object -First 1).IPAddress
  if (-not $ip) { $ip = "<server-ip>" }
  Write-Host ""
  Write-Host "EpicPanel installation complete!" -ForegroundColor Green
  Write-Host "  Panel:   http://${ip}:$Port"
  Write-Host "  Task:    Get-ScheduledTask -TaskName $TaskName"
  Write-Host ""
  Write-Host "Open the URL above to finish setup, then install hosting software from the panel."
}

# ---- run with a visible failure message (never silently vanish) ----
try {
  Install-EpicPanel
} catch {
  Write-Host ""
  Write-Host "EpicPanel installation FAILED: $($_.Exception.Message)" -ForegroundColor Red
  Write-Host "See the messages above for details." -ForegroundColor Yellow
  Read-Host "Press Enter to close"
  exit 1
}
