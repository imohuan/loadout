Set-Location 'D:\Code\Git\loadout'

# 直接模拟 Loadout 的调用：cmd = node, base = cli.mjs, args 由 listAllOnce 拼
# listAllOnce 拼的是: --list all --json [--enable-vision] [--source <sync.json 里的 source>]
# 先从 sync.json 读出 source（和 sourceFromSync 一样）
$syncPath = Join-Path $env:USERPROFILE '.unifyai\sync.json'
Write-Output "sync.json = $syncPath"
if (Test-Path $syncPath) {
  $sc = Get-Content $syncPath -Raw | ConvertFrom-Json
  $src = $sc.source
  Write-Output "sync.json source = $src"
} else {
  Write-Output 'sync.json NOT FOUND'
  $src = $null
}

Write-Output ''
Write-Output '=== 用 sync.json 的 source 跑 --list all（完全复刻服务端） ==='
$argsList = @('--list', 'all', '--json')
if ($src) { $argsList += @('--source', $src) }
Write-Output "args: $($argsList -join ' ')"

$sw = [System.Diagnostics.Stopwatch]::StartNew()
$raw = & node 'D:\Code\Git\unifyai\src\cli.mjs' @argsList 2>&1 | Out-String
$sw.Stop()
Write-Output "elapsed=$($sw.ElapsedMilliseconds)ms"
if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
if ($raw -match '"degradedReason":\s*"([^"]*)"') { $dr = $Matches[1] } else { $dr = '' }
Write-Output "RESULT: rawCount=$rc count=$c degraded=$d"
if ($dr) { Write-Output "reason=$dr" }
