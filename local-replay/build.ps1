# Run from any directory on Windows with Python 3.13 and build requirements installed.
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    python -m PyInstaller --noconfirm --clean --onefile --console `
        --name BC2LocalReplay --paths "$PSScriptRoot" `
        --add-data "$PSScriptRoot/backend/protocol_fixtures.json;backend" `
        --distpath dist --workpath build --specpath build preview.py
    if ($LASTEXITCODE -ne 0) { throw 'PyInstaller build failed' }
    & ./dist/BC2LocalReplay.exe --help
    if ($LASTEXITCODE -ne 0) { throw 'Built executable failed to start' }
    $stage = Join-Path $PSScriptRoot 'build/package'
    if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
    New-Item $stage -ItemType Directory | Out-Null
    Copy-Item dist/BC2LocalReplay.exe, Redirect.ps1, USER_GUIDE.md $stage
    Compress-Archive -Path "$stage/*" -DestinationPath dist/bc2-local-replay-standalone.zip -Force
    $hash = (Get-FileHash dist/bc2-local-replay-standalone.zip -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  bc2-local-replay-standalone.zip" | Set-Content dist/SHA256.txt -Encoding ascii
} finally {
    Pop-Location
}
