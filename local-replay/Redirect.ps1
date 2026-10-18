param([Parameter(Mandatory=$true)][ValidateSet('on','off')][string]$Mode)
$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = @('-NoProfile','-ExecutionPolicy','Bypass','-File',('"'+$PSCommandPath+'"'),'-Mode',$Mode)
    $child = Start-Process powershell.exe -Verb RunAs -ArgumentList $arguments -Wait -PassThru
    exit $child.ExitCode
}
try {
    $path = Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
    $begin = '# === BC2-VIEWERSET-STUB-BEGIN ==='
    $end = '# === BC2-VIEWERSET-STUB-END ==='
    $entry = '127.0.0.1 bfbc2.emulatornexus.com'
    $text = [IO.File]::ReadAllText($path)
    $lines = @($text -split '\r?\n')
    $begins = @($lines | Where-Object { $_ -eq $begin }).Count
    $ends = @($lines | Where-Object { $_ -eq $end }).Count
    if ($begins -ne $ends -or $begins -gt 1) { throw 'Malformed redirect markers; hosts was not changed.' }
    $pattern = '(?m)^'+[regex]::Escape($begin)+'\r?\n'+[regex]::Escape($entry)+'\r?\n'+[regex]::Escape($end)+'(?:\r?\n|$)'
    if ($begins -eq 1 -and -not [regex]::IsMatch($text,$pattern)) { throw 'Unexpected redirect block; hosts was not changed.' }
    $without = [regex]::Replace($text,$pattern,'')
    $conflicts = @($without -split '\r?\n' | Where-Object { $_ -notmatch '^\s*#' -and ($_ -split '#',2)[0] -match '(?i)(?:^|\s)bfbc2\.emulatornexus\.com(?:\s|$)' })
    if ($Mode -eq 'on' -and $conflicts.Count) { throw 'Existing non-preview hostname mapping; resolve it manually. No changes made.' }
    if ($Mode -eq 'on' -and $begins -eq 1) { Write-Host 'Local redirect already enabled.'; exit 0 }
    if ($Mode -eq 'off' -and $begins -eq 0) { Write-Host 'No preview redirect found; no changes made.'; exit 0 }
    $runtime = Join-Path $PSScriptRoot 'runtime'
    [IO.Directory]::CreateDirectory($runtime) | Out-Null
    $backup = Join-Path $runtime ('hosts-before-'+[DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fffffff')+'.private.bak')
    [IO.File]::Copy($path,$backup,$false)
    if ($Mode -eq 'off') { $updated = $without }
    else { $updated = $text; if ($updated.Length -gt 0 -and -not $updated.EndsWith("`n")) { $updated += "`r`n" }; $updated += $begin+"`r`n"+$entry+"`r`n"+$end+"`r`n" }
    # Keep unrelated hosts text. Original bytes are retained in the private backup.
    [IO.File]::WriteAllText($path,$updated,(New-Object Text.UTF8Encoding($false)))
    & ipconfig /flushdns | Out-Null
    Write-Host ('Preview redirect '+$Mode+'. Restart BC2 when changing modes.')
    Write-Host 'Only this helper was elevated. Do not publish runtime backups.'
} catch {
    Write-Error $_
    exit 1
}
