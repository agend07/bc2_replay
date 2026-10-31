# Reproducible offline build of builder.exe (stdlib only; GOPROXY=off, so no network is ever touched).
# Usage: powershell -File build.ps1 [-Version 0.1.0] [-Out builder.exe] [-FreshCache]
# Same Go toolchain + same sources + same -Version -> byte-identical exe (trimpath, no VCS stamp, empty build id).
param(
    [string]$Version = "0.0.0-dev",
    [string]$Out = "builder.exe",
    [switch]$FreshCache  # build with an empty temporary GOCACHE (proves the result does not depend on the cache)
)
$ErrorActionPreference = "Stop"
$env:GOPROXY = "off"; $env:GOSUMDB = "off"; $env:GOTOOLCHAIN = "local"
$env:CGO_ENABLED = "0"; $env:GOAMD64 = "v1"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { $env:PATH = "C:\Program Files\Go\bin;" + $env:PATH }
$cache = $null
if ($FreshCache) {
    $cache = Join-Path ([IO.Path]::GetTempPath()) ("builder_gocache_" + [guid]::NewGuid().ToString("N"))
    $env:GOCACHE = $cache
}
Push-Location $PSScriptRoot
try {
    go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=$Version" -o $Out ./cmd/builder
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $h = -join ($sha.ComputeHash([IO.File]::ReadAllBytes((Resolve-Path $Out))) | ForEach-Object { $_.ToString("x2") }) } finally { $sha.Dispose() }
    $h + "  " + $Out  # .NET hash: Get-FileHash is missing when PSModulePath comes from another PowerShell
} finally {
    Pop-Location
    if ($cache) { Remove-Item -Recurse -Force $cache -ErrorAction SilentlyContinue }
}
