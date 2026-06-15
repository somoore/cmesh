#Requires -Version 5.1
<#
.SYNOPSIS
    Install cmesh on Windows (PowerShell parity for scripts/install.sh).

.DESCRIPTION
    Installs the cmesh binary, then (unless -NoAgentSetup) launches the native
    interactive agent picker so you go straight from one command into "pick your
    agent". Mirrors scripts/install.sh behavior and environment variables.

    Install sources are tried in this order:
      1. $env:CMESH_BIN   - path to a prebuilt cmesh.exe to install
      2. source checkout  - builds from ./cmd/cmesh with `go build` (needs Go)
      3. GitHub release    - downloads a published release from somoore/cmesh

.PARAMETER NoAgentSetup
    Install the binary only; do not auto-launch the agent picker.
    Equivalent to install.sh --no-agent-setup.

.ENVIRONMENT
    CMESH_INSTALL_DIR  Override the install directory (default: ~\.local\bin).
    CMESH_BIN          Path to a prebuilt cmesh.exe to install.
    CMESH_REPO         owner/repo to download a published release from (default: somoore/cmesh).
    CMESH_VERSION      Release tag to download, or "latest" (default).

.EXAMPLE
    .\scripts\install.ps1

.EXAMPLE
    .\scripts\install.ps1 -NoAgentSetup
#>
[CmdletBinding()]
param(
    [switch]$NoAgentSetup
)

# Match `set -eu`: stop on the first error and treat unset-then-used as errors.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not $env:CMESH_REPO) {
    $env:CMESH_REPO = 'somoore/cmesh'
}

function Die {
    param([Parameter(Mandatory)][string]$Message)
    [Console]::Error.WriteLine("cmesh install: $Message")
    exit 1
}

function Info {
    param([string]$Message = '')
    Write-Host "cmesh install: $Message"
}

function Need-Cmd {
    param([Parameter(Mandatory)][string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        Die "required command not found: $Name"
    }
}

function Get-Arch {
    # PROCESSOR_ARCHITECTURE reflects the running process; on 64-bit PowerShell
    # this is correct. AMD64 -> amd64, ARM64 -> arm64.
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { 'amd64'; break }
        'ARM64' { 'arm64'; break }
        default { Die "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
    }
}

# Atomically install $Src to $Dst: copy to a temp sibling, then move into place.
function Install-File {
    param(
        [Parameter(Mandatory)][string]$Src,
        [Parameter(Mandatory)][string]$Dst
    )
    $dir = Split-Path -Parent $Dst
    if (-not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    $tmp = "$Dst.tmp.$PID"
    Copy-Item -LiteralPath $Src -Destination $tmp -Force
    Move-Item -LiteralPath $tmp -Destination $Dst -Force
}

function Install-FromBinary {
    if (-not $env:CMESH_BIN) { return $false }
    if (-not (Test-Path -LiteralPath $env:CMESH_BIN -PathType Leaf)) {
        Die "CMESH_BIN does not name a file: $($env:CMESH_BIN)"
    }
    Install-File -Src $env:CMESH_BIN -Dst $script:InstallPath
    return $true
}

# Returns a directory that contains .\cmd\cmesh, checking the current working
# directory first and then the script's own parent (so running the script from
# inside scripts\ still finds the repo root). Returns $null when not found.
function Get-SourceCheckoutDir {
    if (Test-Path -LiteralPath '.\cmd\cmesh' -PathType Container) {
        return (Get-Location).Path
    }
    $scriptDir = $PSScriptRoot
    if ($scriptDir -and (Test-Path -LiteralPath (Join-Path $scriptDir '..\cmd\cmesh') -PathType Container)) {
        return (Resolve-Path (Join-Path $scriptDir '..')).Path
    }
    return $null
}

function Install-FromSource {
    $src = Get-SourceCheckoutDir
    if (-not $src) { return $false }
    Need-Cmd go
    $tmpdir = Join-Path ([System.IO.Path]::GetTempPath()) "cmesh-install.$PID"
    New-Item -ItemType Directory -Path $tmpdir -Force | Out-Null
    try {
        Info "building cmesh from source in $src"
        $out = Join-Path $tmpdir 'cmesh.exe'
        Push-Location $src
        try {
            & go build -trimpath -o $out ./cmd/cmesh
            if ($LASTEXITCODE -ne 0) { Die "go build failed (exit $LASTEXITCODE)" }
        } finally {
            Pop-Location
        }
        Install-File -Src $out -Dst $script:InstallPath
    } finally {
        Remove-Item -LiteralPath $tmpdir -Recurse -Force -ErrorAction SilentlyContinue
    }
    return $true
}

# Prints the newest release tag for $Repo, or $null on failure. Must not Die:
# an empty version would build a broken .../download//cmesh_... URL.
function Get-LatestVersion {
    param([Parameter(Mandatory)][string]$Repo)
    $url = "https://api.github.com/repos/$Repo/releases/latest"
    try {
        $resp = Invoke-RestMethod -Uri $url -Headers @{ 'User-Agent' = 'cmesh-install' } -ErrorAction Stop
        if ($resp -and $resp.tag_name) { return [string]$resp.tag_name }
    } catch {
        # GitHub's /releases/latest excludes pre-releases. Fall through to the
        # release list so pre-alpha users can still install with "latest".
    }
    try {
        $releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases" -Headers @{ 'User-Agent' = 'cmesh-install' } -ErrorAction Stop
    } catch {
        return $null
    }
    foreach ($release in @($releases)) {
        if ($release -and $release.tag_name) { return [string]$release.tag_name }
    }
    return $null
}

# Downloads a prebuilt cmesh from a GitHub release. Release asset naming matches
# install.sh: cmesh_windows_<arch>.tar.gz.
function Install-FromRelease {
    Need-Cmd tar
    $arch = Get-Arch
    $version = $env:CMESH_VERSION
    if (-not $version -or $version -eq 'latest') {
        $version = Get-LatestVersion -Repo $env:CMESH_REPO
        if (-not $version) {
            Die "could not determine the latest release for $($env:CMESH_REPO) (set CMESH_VERSION=vX.Y.Z, CMESH_BIN=C:\path\to\cmesh.exe, or run from a source checkout)"
        }
    }
    $asset = "cmesh_windows_$arch.tar.gz"
    $url = "https://github.com/$($env:CMESH_REPO)/releases/download/$version/$asset"
    $tmpdir = Join-Path ([System.IO.Path]::GetTempPath()) "cmesh-install.$PID"
    New-Item -ItemType Directory -Path $tmpdir -Force | Out-Null
    try {
        Info "downloading $url"
        $archivePath = Join-Path $tmpdir $asset
        try {
            Invoke-WebRequest -Uri $url -OutFile $archivePath -UseBasicParsing -ErrorAction Stop
        } catch {
            Die "download failed: $url"
        }
        & tar -xzf $archivePath -C $tmpdir
        if ($LASTEXITCODE -ne 0) { Die "failed to extract $asset" }
        $bin = Join-Path $tmpdir 'cmesh.exe'
        if (-not (Test-Path -LiteralPath $bin -PathType Leaf)) {
            # Some archives may ship the binary without the .exe suffix.
            $alt = Join-Path $tmpdir 'cmesh'
            if (Test-Path -LiteralPath $alt -PathType Leaf) { $bin = $alt }
            else { Die "release archive did not contain cmesh binary" }
        }
        Install-File -Src $bin -Dst $script:InstallPath
    } finally {
        Remove-Item -LiteralPath $tmpdir -Recurse -Force -ErrorAction SilentlyContinue
    }
    return $true
}

# --- Resolve install location -------------------------------------------------
# Mirror selfinstall.go: default to ~\.local\bin (CMESH_INSTALL_DIR overrides),
# so the binary's own self-install PATH logic stays consistent on Windows.
$InstallDir = $env:CMESH_INSTALL_DIR
if (-not $InstallDir) {
    $InstallDir = Join-Path $HOME '.local\bin'
}
$script:InstallPath = Join-Path $InstallDir 'cmesh.exe'

# --- Install from the first available source ----------------------------------
if (Install-FromBinary) {
    Info "installed $script:InstallPath from CMESH_BIN"
} elseif (Install-FromSource) {
    Info "installed $script:InstallPath from source"
} elseif (Install-FromRelease) {
    Info "installed $script:InstallPath from GitHub release"
} else {
    [Console]::Error.WriteLine("cmesh install: no install source found.")
    [Console]::Error.WriteLine("  Most likely you ran this from the scripts\ directory. Run it from the repo root:")
    [Console]::Error.WriteLine("    cd ..; .\scripts\install.ps1")
    [Console]::Error.WriteLine("  Or provide a source explicitly:")
    [Console]::Error.WriteLine('    $env:CMESH_BIN="C:\path\to\cmesh.exe"; .\scripts\install.ps1   # install a prebuilt binary')
    [Console]::Error.WriteLine('    $env:CMESH_REPO="owner/repo"; .\scripts\install.ps1            # download a published release')
    exit 1
}

# Print the installed version so the user can confirm the binary works.
if (Test-Path -LiteralPath $script:InstallPath -PathType Leaf) {
    Write-Host -NoNewline "cmesh install: version "
    try {
        & $script:InstallPath version
        if ($LASTEXITCODE -ne 0) { Write-Host "(unknown)" }
    } catch {
        Write-Host "(unknown)"
    }
}

# --- PATH handling ------------------------------------------------------------
# Is the install dir on PATH for the *current* process?
$pathEntries = $env:PATH -split ';' | ForEach-Object { $_.TrimEnd('\') }
$onPath = $pathEntries -contains $InstallDir.TrimEnd('\')

if (-not $onPath) {
    # Persist to the user PATH (registry) idempotently, like setx but without the
    # 1024-char truncation. Then the entry is permanent; current shell still needs
    # a restart for it to take effect.
    $userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
    $userEntries = @()
    if ($userPath) { $userEntries = $userPath -split ';' | ForEach-Object { $_.TrimEnd('\') } }
    if ($userEntries -notcontains $InstallDir.TrimEnd('\')) {
        $newUserPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable('PATH', $newUserPath, 'User')
        Info "added $InstallDir to your user PATH"
        Info "  open a NEW terminal for it to take effect"
    }
    # Make it usable in this very session too.
    $env:PATH = "$env:PATH;$InstallDir"
}

# How the user invokes cmesh from here on: bare name once PATH is active, else the
# full install path (a fresh shell is needed before the new PATH entry resolves).
$CmeshCmd = if ($onPath) { 'cmesh' } else { $script:InstallPath }

function Show-NextSteps {
    Write-Host ""
    Write-Host "============================================================"
    Write-Host "  What to do next"
    Write-Host "============================================================"
    Write-Host ""
    Write-Host "  1. Register the cmesh MCP into your AI coding agent(s):"
    Write-Host "         $CmeshCmd install"
    Write-Host ""
    Write-Host "  2. Open a NEW session in that agent and type exactly:"
    Write-Host ""
    Write-Host "         help me setup cmesh"
    Write-Host ""
    Write-Host "     cmesh guides the rest from inside the agent (detects your"
    Write-Host "     tailnet, writes config, starts the service, links a peer,"
    Write-Host "     shows example prompts). Just answer the yes/no prompts."
    Write-Host "============================================================"
}

# Auto-launch the native interactive agent picker so the user goes straight into
# "pick your agent" - unless they opted out, or this isn't an interactive console
# (where we cannot drive the picker). The picker prints its own next-steps banner.
$interactive = -not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected

if ($NoAgentSetup) {
    Info ""
    Info "Binary installed. Agent setup skipped (-NoAgentSetup)."
    Show-NextSteps
} elseif ($interactive) {
    Info ""
    Info "Launching the cmesh agent picker..."
    Info ""
    & $script:InstallPath install
} else {
    Info ""
    Info "Binary installed. The terminal can't run the interactive picker here."
    Show-NextSteps
}
