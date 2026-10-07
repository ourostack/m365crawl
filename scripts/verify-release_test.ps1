#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Run the real verifier offline, stopping immediately after its checksum gate.
function gh {
    if ($args[0] -eq 'release' -and $args[1] -eq 'view') {
        return 'false'
    }
    if ($args[0] -ne 'release' -or $args[1] -ne 'download') {
        throw "unexpected gh call: $args"
    }
    $work = $args[[array]::IndexOf($args, '-D') + 1]
    $zipName = 'm365crawl_0.2.0_windows_amd64.zip'
    $zip = Join-Path $work $zipName
    [System.IO.File]::WriteAllText($zip, 'synthetic release asset')
    $hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    switch ($testCase) {
        'single match' { $lines = @("$hash  $zipName") }
        'one match among other assets' { $lines = @("$hash  other.zip", "$hash  $zipName") }
        'mismatch' { $lines = @("$('0' * 64)  $zipName") }
        'missing' { $lines = @("$hash  other.zip") }
        'duplicate' { $lines = @("$hash  $zipName", "$hash  $zipName") }
    }
    Set-Content (Join-Path $work 'checksums.txt') $lines
}

function Expand-Archive {
    throw 'test: checksum gate passed'
}

$saved = @{}
$work = Join-Path (Split-Path -Parent $PSScriptRoot) '.ci-tmp\verify-release-amd64'
if (Test-Path $work) { throw "test work directory already exists: $work" }
foreach ($name in 'TAG', 'REPO', 'EXPECT_COMMIT', 'TARGET_ARCH') {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name)
}
$failures = @()
try {
    $env:TAG = 'v0.2.0'
    $env:REPO = 'test/synthetic'
    $env:EXPECT_COMMIT = 'synthetic'
    $env:TARGET_ARCH = 'amd64'
    foreach ($testCase in 'single match', 'one match among other assets', 'mismatch', 'missing', 'duplicate') {
        $expected = switch ($testCase) {
            'mismatch' { 'checksum mismatch for windows_amd64' }
            'missing' { 'checksums.txt has no entry for windows_amd64' }
            'duplicate' { 'checksums.txt has no entry for windows_amd64' }
            default { 'test: checksum gate passed' }
        }
        $actual = 'verifier unexpectedly completed'
        try { & (Join-Path $PSScriptRoot 'verify-release.ps1') }
        catch { $actual = $_.Exception.Message }
        if ($actual -ne $expected) {
            $failures += "${testCase}: expected '$expected', got '$actual'"
        } else {
            Write-Host "PASS: $testCase"
        }
    }
}
finally {
    foreach ($name in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($name, $saved[$name])
    }
    if (Test-Path $work) { Remove-Item -Recurse -Force $work }
}
if ($failures.Count) { throw ($failures -join "`n") }
Write-Host 'verify-release checksum tests passed'
