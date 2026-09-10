Set-Location 'D:\Code\Git\loadout'

$cool = 150
Write-Output "=== cooling proxy for $cool s ==="
Start-Sleep -Seconds $cool

Write-Output "=== proxy cold latency ==="
$cfg = Get-Content 'C:\Users\Administrator\.opencodex\config.json' -Raw | ConvertFrom-Json
$key = $null
if ($cfg.apiKeys -and $cfg.apiKeys.Count -gt 0) { $key = $cfg.apiKeys[0].key }
$h = @{ 'Content-Type' = 'application/json' }
if ($key) { $h['x-opencodex-api-key'] = $key; $h['Authorization'] = "Bearer $key" }
$sw = [System.Diagnostics.Stopwatch]::StartNew()
try {
  $r = Invoke-WebRequest -Uri 'http://localhost:10100/v1/models' -Headers $h -TimeoutSec 60 -UseBasicParsing
  $sw.Stop()
  $n = (($r.Content | ConvertFrom-Json).data | Measure-Object).Count
  Write-Output "proxy cold: HTTP $($r.StatusCode) models=$n elapsed=$($sw.ElapsedMilliseconds)ms"
} catch {
  $sw.Stop()
  Write-Output "proxy cold: ERR elapsed=$($sw.ElapsedMilliseconds)ms"
}

Write-Output ""
Write-Output "=== end-to-end against NEW binary on :3111 ==="
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3111
