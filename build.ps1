# Windows, Python 3.13 and requirements-build.txt. No game files required.
# -BuilderExe: a builder.exe (builder_go/build.ps1 output, e.g. the CI artifact) placed into the package as
# builder/builder.exe; without it the package has no builder (release_pack.py assembles the full release zip).
param([ValidateSet('onedir','onefile')][string]$BundleMode = 'onedir', [string]$BuilderExe = '')
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    $output = Join-Path $PSScriptRoot "dist/$BundleMode"
    $work = Join-Path $PSScriptRoot "build/$BundleMode"
    python -m PyInstaller --noconfirm --clean "--$BundleMode" --noupx --console `
        --name BC2LocalReplay --paths "$PSScriptRoot" `
        --add-data "$PSScriptRoot/backend/protocol_fixtures.json;backend" `
        --distpath "$output" --workpath "$work" --specpath "$work" preview.py
    if ($LASTEXITCODE -ne 0) { throw 'PyInstaller build failed' }
    if ($BundleMode -eq 'onedir') {
        $app = Join-Path $output 'BC2LocalReplay'
    } else {
        $app = $output
    }
    & "$app/BC2LocalReplay.exe" --help
    if ($LASTEXITCODE -ne 0) { throw 'Built executable failed to start' }
    $stage = Join-Path $PSScriptRoot "build/package-$BundleMode"
    if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
    New-Item $stage -ItemType Directory | Out-Null
    Copy-Item "$app/BC2LocalReplay.exe", Redirect.ps1, USER_GUIDE.md $stage
    if ($BuilderExe) {
        New-Item (Join-Path $stage 'builder') -ItemType Directory | Out-Null
        Copy-Item $BuilderExe (Join-Path $stage 'builder/builder.exe')
        & (Join-Path $stage 'builder/builder.exe') version --json
        if ($LASTEXITCODE -ne 0) { throw 'builder.exe failed to start' }
    }
    if ($BundleMode -eq 'onedir') {
        Copy-Item "$app/_internal" $stage -Recurse
    }
    $zip = Join-Path $output 'bc2-local-replay-standalone.zip'
    Compress-Archive -Path "$stage/*" -DestinationPath $zip -Force
    $hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  bc2-local-replay-standalone.zip" | Set-Content "$output/SHA256.txt" -Encoding ascii
    Write-Host "Bundle mode: $BundleMode; ZIP: $zip"
} finally {
    Pop-Location
}
