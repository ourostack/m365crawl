#!/usr/bin/env pwsh
param(
    [string]$DistDir = (Join-Path (Split-Path -Parent $PSScriptRoot) 'dist')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not (Test-Path $DistDir)) {
    throw "dist directory not found: $DistDir"
}

$required = @(
    'm365crawl_*_darwin_amd64.tar.gz',
    'm365crawl_*_darwin_arm64.tar.gz',
    'm365crawl_*_windows_amd64.zip',
    'm365crawl_*_windows_arm64.zip'
)

$artifacts = @()
foreach ($pattern in $required) {
    $matches = @(Get-ChildItem -Path $DistDir -Filter $pattern -File)
    if ($matches.Count -ne 1) {
        throw "expected exactly one artifact matching $pattern in $DistDir, found $($matches.Count)"
    }
    $artifacts += $matches[0]
}

$version = $null
foreach ($artifact in $artifacts) {
    if ($artifact.Name -notmatch '^m365crawl_(?<version>.+)_(darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)$') {
        throw "artifact name has an unexpected shape: $($artifact.Name)"
    }
    if (-not $version) {
        $version = $Matches.version
    } elseif ($Matches.version -ne $version) {
        throw "artifact versions differ: expected $version, saw $($artifact.Name)"
    }
}

$checksums = Join-Path $DistDir 'checksums.txt'
if (-not (Test-Path $checksums)) {
    throw "checksums.txt not found in $DistDir"
}

$lines = Get-Content $checksums
foreach ($artifact in $artifacts) {
    $entries = @($lines | Where-Object { $_ -match "  $([regex]::Escape($artifact.Name))$" })
    if ($entries.Count -ne 1) {
        throw "checksums.txt has no entry for $($artifact.Name)"
    }
    $actual = (Get-FileHash -Path $artifact.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    $expected = ($entries[0] -split '\s+')[0].ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "checksum mismatch for $($artifact.Name): expected $expected, got $actual"
    }
}

Write-Host "release artifacts OK: $version"
