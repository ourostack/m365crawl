#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$tmpRoot = Join-Path $repoRoot '.ci-tmp\coverage-windows'
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

$go = Get-Command go -ErrorAction SilentlyContinue
if ($go) {
    $goExe = $go.Source
} else {
    $goExe = Join-Path $HOME '.local\sdk\go\bin\go.exe'
    if (-not (Test-Path $goExe)) {
        throw 'scripts/check-coverage.ps1 requires go.exe in PATH or at ~/.local/sdk/go/bin/go.exe.'
    }
}

$packages = if ([string]::IsNullOrWhiteSpace($env:COVERAGE_PACKAGES)) { './internal/...' } else { $env:COVERAGE_PACKAGES }
$profile = Join-Path $tmpRoot 'cover.out'
$log = Join-Path $tmpRoot 'go-test.log'
$stderrLog = Join-Path $tmpRoot 'go-test.stderr.log'

Set-Location $repoRoot
$env:GOWORK = 'off'

$packageArgs = $packages -split '\s+' | Where-Object { $_ }
$argList = @('test', '-count=1', "-coverprofile=$profile") + $packageArgs
$proc = Start-Process -FilePath $goExe -ArgumentList $argList -NoNewWindow -Wait -PassThru -RedirectStandardOutput $log -RedirectStandardError $stderrLog
if ($proc.ExitCode -ne 0) {
    Get-Content $log
    if (Test-Path $stderrLog) {
        Get-Content $stderrLog
    }
    throw 'check-coverage: go test failed'
}

$requiredFiles = @(
    'internal/cli/platform_windows.go',
    'internal/errs/no_full_disk_access_windows.go',
    'internal/store/lock_windows.go',
    'internal/store/security_windows.go',
    'internal/teamsdesktop/defaultroot_windows.go',
    'internal/teamsdesktop/security_windows.go'
)

$coveredFiles = @{}
Get-Content $profile | Select-Object -Skip 1 | ForEach-Object {
    $parts = $_ -split ' '
    if ($parts.Length -lt 3) {
        return
    }
    $path = ($parts[0] -split ':')[0]
    $count = [int]$parts[-1]
    if ($path -match 'github\.com/ourostack/teamscrawl/(?<rel>internal/.+)$' -and $count -gt 0) {
        $coveredFiles[$Matches.rel] = $true
    }
}

$missing = @($requiredFiles | Where-Object { -not $coveredFiles.ContainsKey($_) })
if ($missing.Count -gt 0) {
    throw "Windows coverage missing execution for: $($missing -join ', ')"
}

Write-Host 'coverage OK: Windows-only files executed under go test coverage'
