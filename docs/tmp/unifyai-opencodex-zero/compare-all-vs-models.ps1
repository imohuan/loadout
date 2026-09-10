Set-Location 'D:\Code\Git\loadout'

Write-Output '=== CLI: --list all (exactly what /all runs) ==='
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$raw = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source 'C:\Users\Administrator\.opencodex\config.json' 2>&1 | Out-String
$sw.Stop()
Write-Output "elapsed=$($sw.ElapsedMilliseconds)ms"

if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
Write-Output "all: rawCount=$rc count=$c degraded=$d"

Write-Output ''
Write-Output '=== CLI: --list models (what /opencodex-models runs) ==='
$sw2 = [System.Diagnostics.Stopwatch]::StartNew()
$raw2 = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list models --json 2>&1 | Out-String
$sw2.Stop()
Write-Output "elapsed=$($sw2.ElapsedMilliseconds)ms"
if ($raw2 -match '"rawCount":\s*(\d+)') { $rc2 = $Matches[1] } else { $rc2 = '?' }
if ($raw2 -match '"count":\s*(\d+)') { $c2 = $Matches[1] } else { $c2 = '?' }
if ($raw2 -match '"degraded":\s*(\w+)') { $d2 = $Matches[1] } else { $d2 = '?' }
Write-Output "models: rawCount=$rc2 count=$c2 degraded=$d2"
