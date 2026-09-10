param([int]$Cool = 130, [string]$Tag = "run")

Write-Output "cooling proxy for $Cool s..."
Start-Sleep -Seconds $Cool

$sw = [System.Diagnostics.Stopwatch]::StartNew()
$raw = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source 'C:\Users\Administrator\.opencodex\config.json' 2>&1 | Out-String
$sw.Stop()

if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
if ($raw -match '"degradedReason":\s*"([^"]*)"') { $dr = $Matches[1] } else { $dr = '' }

Write-Output "[$Tag] rawCount=$rc count=$c degraded=$d elapsed=$($sw.ElapsedMilliseconds)ms"
if ($dr) { Write-Output "[$Tag] reason=$dr" }
