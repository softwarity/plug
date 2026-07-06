# plug — Windows installer (PowerShell parity of the unix `install` branch).
#
# Invoke it straight from the cluster, exactly like the unix one-liner:
#
#   ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null `
#       get@<cluster-host> install-windows | powershell -NoProfile -Command -
#
# The agent's `serve-binary` ForceCommand streams *this file* on `install-windows`
# (it is versioned in the repo and reviewable). It:
#   1. reads host/port off the live `ssh ... get@<host> install-windows` you are
#      running (same trick as unix: that ssh is still in the process table while
#      this script streams), and pre-creates ~/.plug/<host>.conf so the first run
#      needs no wizard;
#   2. downloads plug-windows-amd64.exe from the agent over ssh (`get@<host>
#      windows-amd64`, the same raw-binary verb the launcher uses);
#   3. downloads wintun.dll (WinTUN needs it next to the exe) and drops it beside
#      plug.exe;
#   4. installs both into %LOCALAPPDATA%\Programs\plug and adds that dir to the
#      user PATH if missing.
#
# NO admin is required to INSTALL. But note (documented in the README): creating
# the WinTUN adapter + routes needs Administrator, and unlike Linux setcap /
# macOS setuid there is no binary bit to pre-authorize that — so for now each
# `plug <command>` session must be started from an elevated (Administrator)
# terminal. See the tail of this script and the README "Windows" section.

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# WinTUN release used by the datapath (matches scripts/selftest.sh).
$WintunUrl     = 'https://www.wintun.net/builds/wintun-0.14.1.zip'
$WintunArch    = 'amd64'
$DefaultPort   = '2222'
$InstallDir    = Join-Path $env:LOCALAPPDATA 'Programs\plug'
$PlugExe       = Join-Path $InstallDir 'plug.exe'
$WintunDll     = Join-Path $InstallDir 'wintun.dll'

function Info($msg) { Write-Host "plug: $msg" }
function Die($msg)  { Write-Error "plug: $msg"; exit 1 }

# --- 1. Discover the cluster host/port from the live ssh invocation ----------
# You reached the agent with `ssh ... get@<host> install-windows`; the agent can
# never know which address that was (a Swarm routing mesh hides it), but your own
# ssh line does — and it is still running (blocked streaming this script). Read
# host/port off it, best-effort. Naming the profile by host means a second
# cluster (get@node1) adds a second profile instead of being ignored, so
# `plug -p node0` and `plug -p node1` run side by side.
$SshHost = $null
$SshPort = $DefaultPort
$profileMsg = $null
try {
    $line = Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" |
            Where-Object { $_.CommandLine -match 'get@' -and $_.CommandLine -match 'install-windows' } |
            Select-Object -First 1 -ExpandProperty CommandLine
    if ($line) {
        if ($line -match 'get@([A-Za-z0-9._-]+)') { $SshHost = $Matches[1] }
        if ($line -match '-p[ =]*([0-9]+)')       { $SshPort = $Matches[1] }
    }
} catch { }

if ($SshHost) {
    $plugCfgDir = Join-Path $env:USERPROFILE '.plug'
    $conf       = Join-Path $plugCfgDir "$SshHost.conf"
    if (Test-Path -LiteralPath $conf) {
        $profileMsg = "'$SshHost' (already configured)"
    } else {
        New-Item -ItemType Directory -Force -Path $plugCfgDir | Out-Null
        # Match the exact format the unix installer / wizard writes: "host = ..\nport = ..\n".
        "host = $SshHost`nport = $SshPort`n" | Set-Content -LiteralPath $conf -NoNewline -Encoding ASCII
        $profileMsg = "'$SshHost' -> ${SshHost}:$SshPort"
    }
}

# --- 2. Download plug-windows-amd64.exe from the agent ------------------------
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

function Get-AgentBinary($outFile) {
    if (-not $SshHost) {
        Die 'cannot download plug.exe: no cluster host detected (run from an `ssh ... get@<host> install-windows | powershell -` line)'
    }
    if (-not (Get-Command ssh -ErrorAction SilentlyContinue)) {
        Die 'ssh.exe not found. Install the Windows OpenSSH client (Settings > Apps > Optional Features > OpenSSH Client), then re-run.'
    }
    Info "downloading plug.exe from $SshHost ..."
    # Same raw-binary verb the launcher's getDownload uses. ssh writes the binary
    # to stdout; Start-Process -RedirectStandardOutput does a byte-exact redirect
    # to the file (no newline mangling) and is Windows-PowerShell-5.1 compatible
    # (ProcessStartInfo.ArgumentList is not). stderr goes to a temp file.
    $errFile = Join-Path ([System.IO.Path]::GetTempPath()) ("plug-ssh-" + [guid]::NewGuid().ToString('N') + '.err')
    $sshArgs = @('-p', $SshPort,
                 '-o', 'StrictHostKeyChecking=no',
                 '-o', 'UserKnownHostsFile=NUL',
                 '-o', 'LogLevel=ERROR',
                 '-o', 'BatchMode=yes',
                 "get@$SshHost", 'windows-amd64')
    $p = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -NoNewWindow -Wait -PassThru `
                       -RedirectStandardOutput $outFile -RedirectStandardError $errFile
    $stderr = ''
    if (Test-Path -LiteralPath $errFile) {
        $stderr = (Get-Content -LiteralPath $errFile -Raw -ErrorAction SilentlyContinue)
        Remove-Item -LiteralPath $errFile -ErrorAction SilentlyContinue
    }
    if ($p.ExitCode -ne 0) {
        Remove-Item -LiteralPath $outFile -ErrorAction SilentlyContinue
        Die "download failed (ssh exit $($p.ExitCode)): $($stderr.Trim())"
    }
    if (-not (Test-Path -LiteralPath $outFile)) {
        Die "download produced no file"
    }
    $len = (Get-Item -LiteralPath $outFile).Length
    if ($len -lt 1MB) {
        Remove-Item -LiteralPath $outFile -ErrorAction SilentlyContinue
        Die "downloaded plug.exe looks invalid ($len bytes): $($stderr.Trim())"
    }
    # Sanity-check the PE magic ("MZ"), mirroring the launcher's looksLikeBinary.
    $fh = [System.IO.File]::OpenRead($outFile)
    try {
        $b0 = $fh.ReadByte(); $b1 = $fh.ReadByte()
    } finally {
        $fh.Close()
    }
    if ($b0 -ne 0x4D -or $b1 -ne 0x5A) {
        Remove-Item -LiteralPath $outFile -ErrorAction SilentlyContinue
        Die "downloaded plug.exe is not a Windows executable (bad PE header)"
    }
}

Get-AgentBinary $PlugExe
Info "installed to $PlugExe"

# --- 3. Download wintun.dll and drop it beside plug.exe ----------------------
# WinTUN loads wintun.dll from next to the binary; without it adapter creation
# fails. Fetch the same build the selftest uses.
if (Test-Path -LiteralPath $WintunDll) {
    Info "wintun.dll already present"
} else {
    Info "downloading wintun.dll ..."
    $tmpZip = Join-Path ([System.IO.Path]::GetTempPath()) ("wintun-" + [guid]::NewGuid().ToString('N') + '.zip')
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("wintun-" + [guid]::NewGuid().ToString('N'))
    try {
        # Force modern TLS on Windows PowerShell 5.x (wintun.net is TLS 1.2+).
        try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch { }
        Invoke-WebRequest -Uri $WintunUrl -OutFile $tmpZip -UseBasicParsing
        Expand-Archive -LiteralPath $tmpZip -DestinationPath $tmpDir -Force
        $dll = Join-Path $tmpDir "wintun\bin\$WintunArch\wintun.dll"
        if (-not (Test-Path -LiteralPath $dll)) {
            Die "wintun.dll not found in the archive (layout changed?)"
        }
        Copy-Item -LiteralPath $dll -Destination $WintunDll -Force
        Info "installed wintun.dll"
    } finally {
        Remove-Item -LiteralPath $tmpZip -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $tmpDir -Recurse -ErrorAction SilentlyContinue
    }
}

# --- 4. Add the install dir to the user PATH if absent -----------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
$onPath = $userPath -split ';' | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }
if (-not $onPath) {
    $newPath = if ($userPath.Trim()) { "$($userPath.TrimEnd(';'));$InstallDir" } else { $InstallDir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    # Reflect it in THIS session too, so a follow-up `plug` works without reopening.
    $env:Path = "$env:Path;$InstallDir"
    Info "added $InstallDir to your PATH (open a new terminal for it to take effect everywhere)"
} else {
    Info "$InstallDir already on PATH"
}

# --- Done: report profile + the admin caveat ---------------------------------
if ($profileMsg) { Info "profile $profileMsg" }

Write-Host ''
Info "ready."
Info "IMPORTANT: creating the WinTUN adapter and routes needs Administrator."
Info "Start plug from an elevated terminal (Run as administrator), then:"
if ($profileMsg) {
    Info "    plug <your command>          (several clusters? plug -p <name> <your command>)"
} else {
    Info "    plug <your command>          (the first run asks for your cluster)"
}
