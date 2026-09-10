Set-Location 'D:\Code\Git\loadout'

Write-Output '=== A) 带字面 ~ 的 source（CLI 是否会炸） ==='
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$rawA = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source '~/.opencodex/config.json' 2>&1 | Out-String
$sw.Stop()
Write-Output "elapsed=$($sw.ElapsedMilliseconds)ms"
Write-Output "--- 原始输出前 600 字符 ---"
Write-Output $rawA.Substring(0, [Math]::Min(600, $rawA.Length))

Write-Output ''
Write-Output '=== B) 带展开后绝对路径的 source ==='
$sw2 = [System.Diagnostics.Stopwatch]::StartNew()
$rawB = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source "$env:USERPROFILE\.opencodex\config.json" 2>&1 | Out-String
$sw2.Stop()
Write-Output "elapsed=$($sw2.ElapsedMilliseconds)ms"
if ($rawB -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($rawB -match 'degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
Write-Output "B RESULT: rawCount=$rc degraded=$d"
