Set-Location 'D:\Code\Git\loadout'

Write-Output '=== 直接对比：完全相同条件下，两条 CLI 命令各跑一次 ==='
Write-Output '(代理现在是热的，两条都应该成功——若 /all 仍失败，就是命令本身的问题)'
Write-Output ''

$src = 'C:\Users\Administrator\.opencodex\config.json'

Write-Output '--- [1] --list all --json --source <abs> ---'
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$rawAll = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source $src 2>&1 | Out-String
$sw.Stop()
Write-Output "elapsed=$($sw.ElapsedMilliseconds)ms"
if ($rawAll -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($rawAll -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($rawAll -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
Write-Output "all:    rawCount=$rc count=$c degraded=$d"

Write-Output ''
Write-Output '--- [2] --list models --json --source <abs> ---'
$sw2 = [System.Diagnostics.Stopwatch]::StartNew()
$rawM = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list models --json --source $src 2>&1 | Out-String
$sw2.Stop()
Write-Output "elapsed=$($sw2.ElapsedMilliseconds)ms"
if ($rawM -match '"rawCount":\s*(\d+)') { $rc2 = $Matches[1] } else { $rc2 = '?' }
if ($rawM -match '"count":\s*(\d+)') { $c2 = $Matches[1] } else { $c2 = '?' }
if ($rawM -match '"degraded":\s*(\w+)') { $d2 = $Matches[1] } else { $d2 = '?' }
Write-Output "models: rawCount=$rc2 count=$c2 degraded=$d2"
