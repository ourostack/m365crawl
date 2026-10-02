#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

foreach ($name in 'TAG', 'REPO', 'EXPECT_COMMIT', 'TARGET_ARCH') {
    if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) {
        throw "$name is required"
    }
}

function Get-PeMachine {
    param([string]$Path)

    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $reader = [System.IO.BinaryReader]::new($stream)
        $stream.Seek(0x3C, [System.IO.SeekOrigin]::Begin) | Out-Null
        $offset = $reader.ReadInt32()
        $stream.Seek($offset + 4, [System.IO.SeekOrigin]::Begin) | Out-Null
        return $reader.ReadUInt16()
    }
    finally {
        $stream.Dispose()
    }
}

$tag = $env:TAG
$repo = $env:REPO
$expectCommit = $env:EXPECT_COMMIT
$targetArch = $env:TARGET_ARCH
$version = $tag.TrimStart('v')

Write-Host '==> Release flags'
$prereleaseExpected = $tag.Contains('-')
$prereleaseActual = gh release view $tag -R $repo --json isPrerelease --jq .isPrerelease
if ([System.Convert]::ToBoolean($prereleaseActual) -ne $prereleaseExpected) {
    throw "isPrerelease is $prereleaseActual for $tag, expected $prereleaseExpected"
}
if ($prereleaseExpected) {
    $latest = gh api "repos/$repo/releases/latest" --jq .tag_name 2>$null
    if ($latest -eq $tag) {
        throw "prerelease $tag is marked latest"
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$work = Join-Path $repoRoot ".ci-tmp\verify-release-$targetArch"
if (Test-Path $work) {
    Remove-Item -Recurse -Force $work
}
New-Item -ItemType Directory -Path $work | Out-Null

$zipName = "teamscrawl_${version}_windows_${targetArch}.zip"

Write-Host "==> Downloading $tag from $repo into $work"
gh release download $tag -R $repo -D $work -p checksums.txt -p $zipName | Out-Null

Write-Host '==> Checksums'
$checksums = Get-Content (Join-Path $work 'checksums.txt')
$entry = $checksums | Where-Object { $_ -match "  $([regex]::Escape($zipName))$" }
if (-not $entry) {
    throw "checksums.txt has no entry for windows_$targetArch"
}
$expected = ($entry[0] -split '\s+')[0].ToLowerInvariant()
$actual = (Get-FileHash -Path (Join-Path $work $zipName) -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) {
    throw "checksum mismatch for windows_$targetArch"
}

Write-Host "==> windows_$targetArch"
$dir = Join-Path $work $targetArch
Expand-Archive -LiteralPath (Join-Path $work $zipName) -DestinationPath $dir -Force
$bin = Get-ChildItem -Path $dir -Filter teamscrawl.exe -Recurse | Select-Object -First 1
if (-not $bin) {
    throw "teamscrawl.exe not found in $zipName"
}

$machine = Get-PeMachine $bin.FullName
$wanted = if ($targetArch -eq 'amd64') { 0x8664 } else { 0xAA64 }
if ($machine -ne $wanted) {
    throw ("PE machine for {0} is 0x{1:X4}, expected 0x{2:X4}" -f $bin.FullName, $machine, $wanted)
}

$signature = Get-AuthenticodeSignature -FilePath $bin.FullName
if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::NotSigned) {
    throw "expected the Windows binary to be unsigned, got Authenticode status $($signature.Status)"
}

Write-Host "==> Running the $targetArch binary"
$out = & $bin.FullName --json version
$json = $out | ConvertFrom-Json
if ($json.version -ne $version) {
    throw "version is '$($json.version)', expected '$version'"
}
if ($json.commit -ne $expectCommit) {
    throw "commit is '$($json.commit)', expected '$expectCommit'"
}

Write-Host "verify-release: $tag verified ($($json.version), $($json.commit), windows_$targetArch)"
