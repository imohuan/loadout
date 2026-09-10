Set-Location 'D:\Code\Git\loadout'

# /all 走的命令（无 --source），/models 走的命令（有 --source）
# 服务端是「代理刚被第一次问」时先跑 /all。这里冷却后按同样顺序来一遍。
Write-Output '=== cooling proxy 150s ==='
Start-Sleep -Seconds 150

Write-Output ''
Write-Output '=== [1] 先跑 /all 的命令（无 --source），模拟页面首次加载 ==='
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$raw = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json 2>&1 | Out-String
$sw.Stop()
Write-Output "elapsed=$($sw.ElapsedMilliseconds)ms"
if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
if ($raw -match '"degradedReason":\s*"([^"]*)"') { $dr = $Matches[1] } else { $dr = '' }
Write-Output "all(no-source): rawCount=$rc count=$c degraded=$d"
if ($dr) { Write-Output "  reason=$dr" }

Write-Output ''
Write-Output '=== [2] 紧接着跑 /models 的命令 ==='
$sw2 = [System.Diagnostics.Stopwatch]::StartNew()
$raw2 = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list models --json 2>&1 | Out-String
$sw2.Stop()
Write-Output "elapsed=$($sw2.ElapsedMilliseconds)ms"
if ($raw2 -match '"rawCount":\s*(\d+)') { $rc2 = $Matches[1] } else { $rc2 = '?' }
if ($raw2 -match '"count":\s*(\d+)') { $c2 = $Matches[1] } else { $c2 = '?' }
if ($raw2 -match '"degraded":\s*(\w+)') { $d2 = $Matches[1] } else { $d2 = '?' }
Write-Output "models: rawCount=$rc2 count=$c2 degraded=$d2"
