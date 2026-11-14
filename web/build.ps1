#Requires -Version 5.1
<#
.SYNOPSIS
    Build the SPA into internal\webui\embedded (PowerShell counterpart of build.sh).

.DESCRIPTION
    Same contract as web/build.sh, so the two entry points can be used
    interchangeably: identical output directory handling, identical dependency
    fingerprint, identical BUILD_ID format. The fingerprint algorithm MUST stay
    byte-for-byte compatible with build.sh -- if it drifts, alternating between
    the two scripts makes each one consider the other's install stale and run
    `npm install` every time.

.PARAMETER OutDir
    Output directory. Defaults to internal\webui\embedded. Docker passes an
    explicit -OutDir and copies it into the Go build stage.

.PARAMETER SkipDeps
    Do not touch node_modules at all (same as FW_SKIP_DEPS=1).

.PARAMETER ForceDeps
    Always run a clean `npm ci`, which rewrites node_modules (FW_FORCE_DEPS=1).

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File web\build.ps1

.EXAMPLE
    ./web/build.ps1 -OutDir .\out
#>
[CmdletBinding()]
param(
    [string]$OutDir,
    [switch]$SkipDeps,
    [switch]$ForceDeps
)

$ErrorActionPreference = 'Stop'

$Root = Split-Path -Parent $PSScriptRoot
$WebDir = $PSScriptRoot
if (-not $OutDir) { $OutDir = Join-Path $Root 'internal\webui\embedded' }
$OutDir = [System.IO.Path]::GetFullPath($OutDir)

# Never empty the whole output directory: stub.html and .gitkeep are versioned
# placeholders that must survive a local build.
#
# Delete through .NET rather than Remove-Item: these are build artifacts that do
# not need to be recoverable, and on Windows a shell profile or hook is free to
# redirect Remove-Item to the Recycle Bin, which is exactly the pile-up this
# script is meant to avoid.
$assetsDir = Join-Path $OutDir 'assets'
if (Test-Path -LiteralPath $assetsDir) {
    [System.IO.Directory]::Delete($assetsDir, $true)
}
foreach ($name in @('index.html', 'BUILD_ID')) {
    $p = Join-Path $OutDir $name
    if (Test-Path -LiteralPath $p) { [System.IO.File]::Delete($p) }
}

Push-Location $WebDir
try {
    # ---- toolchain preflight ------------------------------------------------
    # Resolving node is not enough: tool-version managers (mise, asdf) keep a
    # shim on PATH after the version it points at is gone, and the shim only
    # fails once it actually runs. Run it for real, and let its own stderr show
    # through instead of burying it under a generic message.
    $nodeCmd = Get-Command node -ErrorAction SilentlyContinue
    $npmCmd = Get-Command npm -ErrorAction SilentlyContinue
    $nodeOk = $false
    $npmOk = $false
    if ($nodeCmd) {
        & node -e "0"
        if ($LASTEXITCODE -eq 0) { $nodeOk = $true }
    }
    if ($npmCmd) {
        & npm --version *> $null
        if ($LASTEXITCODE -eq 0) { $npmOk = $true }
    }
    if (-not $nodeOk -or -not $npmOk) {
        $nodeShown = if ($nodeCmd) { $nodeCmd.Source } else { '<not found>' }
        $npmShown = if ($npmCmd) { $npmCmd.Source } else { '<not found>' }
        Write-Host 'error: node/npm on PATH is not runnable, so the SPA cannot be built.' -ForegroundColor Red
        Write-Host ''
        Write-Host '  A version-manager shim (mise/asdf) stays on PATH after the version it'
        Write-Host '  points at is gone, so it resolves but cannot execute. Install what the'
        Write-Host '  config asks for, or bypass the shim by putting the real directory first:'
        Write-Host '      mise install'
        Write-Host '      $env:PATH = (mise where node) + ";" + $env:PATH'
        Write-Host '      .\web\build.ps1'
        Write-Host ''
        Write-Host "  node -> $nodeShown"
        Write-Host "  npm  -> $npmShown"
        exit 1
    }

    # ---- dependencies -------------------------------------------------------
    # `npm ci` deletes node_modules before reinstalling it. That costs more time
    # than the build itself, and on Windows the deleted files pile up in the
    # Recycle Bin, so it only runs when there is nothing left to preserve.
    #
    # The fingerprint covers both manifests plus the Node platform/ABI: this same
    # directory is sometimes used from Windows and from WSL, and a node_modules
    # installed by one cannot be loaded by the other, because the per-platform
    # binary packages differ.
    $stamp = Join-Path $WebDir 'node_modules\.cache\feedwatch-deps'

    function Get-DepsFingerprint {
        # Mirrors fingerprint() in build.sh: sha256 over "name\0" + file bytes for
        # each manifest in order, then the "platform-arch-abi" tag. String input
        # is UTF-8 in node's hash.update(), hence UTF8.GetBytes below.
        #
        # The JS is passed with single quotes only: Windows PowerShell 5.1 drops
        # embedded double quotes when it re-quotes an argument for a native
        # program, which silently turns the expression into something else.
        $platformTag = & node -p "process.platform + ' ' + process.arch + ' ' + process.versions.modules"
        if ($LASTEXITCODE -ne 0) { throw 'failed to query the node platform tag' }
        $platformTag = (($platformTag -join ' ').Trim() -replace '\s+', '-')
        if ($platformTag -notmatch '^[a-z0-9]+-[a-z0-9]+-\d+$') {
            throw "unexpected node platform tag: '$platformTag'"
        }

        $sha = [System.Security.Cryptography.SHA256]::Create()
        $buffer = New-Object System.IO.MemoryStream
        try {
            foreach ($name in @('package.json', 'package-lock.json')) {
                $nameBytes = [System.Text.Encoding]::UTF8.GetBytes($name + [char]0)
                $buffer.Write($nameBytes, 0, $nameBytes.Length)
                $path = Join-Path $WebDir $name
                if (Test-Path -LiteralPath $path) {
                    $fileBytes = [System.IO.File]::ReadAllBytes($path)
                    $buffer.Write($fileBytes, 0, $fileBytes.Length)
                }
            }
            $tagBytes = [System.Text.Encoding]::UTF8.GetBytes($platformTag)
            $buffer.Write($tagBytes, 0, $tagBytes.Length)
            $digest = $sha.ComputeHash($buffer.ToArray())
        }
        finally {
            $buffer.Dispose()
            $sha.Dispose()
        }
        -join ($digest | ForEach-Object { $_.ToString('x2') })
    }

    function Invoke-NpmChecked {
        # Takes the argv as an array: `--no-audit` and `--` must reach npm as
        # literal arguments, and splatting the automatic $args variable does not
        # forward them reliably from a PowerShell function.
        param([string[]]$NpmArgs)
        & npm @NpmArgs
        if ($LASTEXITCODE -ne 0) {
            throw ('npm ' + ($NpmArgs -join ' ') + " failed with exit code $LASTEXITCODE")
        }
    }

    $skip = $SkipDeps -or ($env:FW_SKIP_DEPS -eq '1')
    $force = (-not $skip) -and ($ForceDeps -or ($env:FW_FORCE_DEPS -eq '1'))
    $install = ''

    if ($skip) {
        Write-Host 'deps: skipped (skip requested)'
    }
    elseif ($force) {
        $install = 'ci'
    }
    elseif ((Test-Path -LiteralPath $stamp) -and
            ((Get-Content -LiteralPath $stamp -Raw).Trim() -eq (Get-DepsFingerprint))) {
        Write-Host 'deps: up to date, keeping node_modules'
    }
    elseif (Test-Path -LiteralPath (Join-Path $WebDir 'node_modules')) {
        # Incremental: adds and removes only what the manifests require, so the
        # existing tree survives instead of being rebuilt from scratch.
        $install = 'install'
    }
    else {
        $install = 'ci'
    }

    if ($install -eq 'ci') {
        Write-Host 'deps: clean install, node_modules will be rewritten (npm ci)'
        Invoke-NpmChecked -NpmArgs @('ci', '--no-audit', '--no-fund')
    }
    elseif ($install -eq 'install') {
        Write-Host 'deps: manifests changed, installing incrementally'
        Invoke-NpmChecked -NpmArgs @('install', '--no-audit', '--no-fund', '--prefer-offline')
    }

    if ($install) {
        $cacheDir = Split-Path -Parent $stamp
        if (-not (Test-Path -LiteralPath $cacheDir)) {
            New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
        }
        # Write after installing: npm install may rewrite package-lock.json.
        # LF and no BOM so build.sh reads back the exact same bytes it writes.
        # Set-Content would emit CRLF on Windows PowerShell 5.1, and the shell
        # side only strips the trailing newline, leaving a stray \r in the
        # comparison -- that would make each script think the other is stale.
        $utf8Stamp = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($stamp, (Get-DepsFingerprint) + "`n", $utf8Stamp)
    }

    Invoke-NpmChecked -NpmArgs @('run', 'build', '--', '--outDir', $OutDir)

    # BUILD_ID is exposed by /api/meta so an operator can tell which frontend
    # build is baked into the running binary. Format: MMDDHHMM in the builder's
    # local time plus the git short hash, e.g. "11142242-2f04086". Matches
    # build.sh exactly; git may be absent (then "unknown").
    $shortHash = 'unknown'
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $h = & git rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -eq 0 -and $h) { $shortHash = ([string]$h).Trim() }
    }
    $buildId = (Get-Date -Format 'MMddHHmm') + '-' + $shortHash

    # No BOM, and a trailing newline like build.sh: Go trims space but keeps a BOM.
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText((Join-Path $OutDir 'BUILD_ID'), $buildId + "`n", $utf8NoBom)
    Write-Host "webui assets built into $OutDir ($buildId)"
}
finally {
    Pop-Location
}
