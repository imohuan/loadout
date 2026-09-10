$cfg = Get-Content 'C:\Users\Administrator\.opencodex\config.json' -Raw | ConvertFrom-Json
$key = $null
if ($cfg.apiKeys -and $cfg.apiKeys.Count -gt 0) { $key = $cfg.apiKeys[0].key }

# 故意隔很久不打代理，制造冷状态
Write-Output "cooling proxy for 120s..."
Start-Sleep -Seconds 120

$headers = @{ 'Content-Type' = 'application/json' }
if ($key) { $headers['x-opencodex-api-key'] = $key; $headers['Authorization'] = "Bearer $key" }

# 1) 先看代理本身冷启动要多久
$sw = [System.Diagnostics.Stopwatch]::StartNew()
try {
  $r = Invoke-WebRequest -Uri 'http://localhost:10100/v1/models' -Headers $headers -TimeoutSec 60 -UseBasicParsing
  $sw.Stop()
  $n = (($r.Content | ConvertFrom-Json).data | Measure-Object).Count
  Write-Output "proxy cold call: HTTP $($r.StatusCode) models=$n elapsed=$($sw.ElapsedMilliseconds)ms"
} catch {
  $sw.Stop()
  Write-Output "proxy cold call: ERR elapsed=$($sw.ElapsedMilliseconds)ms $($_.Exception.Message)"
}

# 2) 再用修复后的本地 CLI 源码查一次（代理已热，但验证命令链路通）
$sw2 = [System.Diagnostics.Stopwatch]::StartNew()
$raw = & node 'D:\Code\Git\unifyai\src\cli.mjs' --list all --json --source 'C:\Users\Administrator\.opencodex\config.json' 2>&1 | Out-String
$sw2.Stop()
if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
Write-Output "cli --list all: rawCount=$rc count=$c degraded=$d elapsed=$($sw2.ElapsedMilliseconds)ms"
