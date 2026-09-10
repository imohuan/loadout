$key = $null
$cfg = Get-Content 'C:\Users\Administrator\.opencodex\config.json' -Raw | ConvertFrom-Json
if ($cfg.apiKeys -and $cfg.apiKeys.Count -gt 0) { $key = $cfg.apiKeys[0].key }
$headers = @{ 'Content-Type' = 'application/json' }
if ($key) { $headers['x-opencodex-api-key'] = $key; $headers['Authorization'] = "Bearer $key" }

foreach ($i in 1..5) {
  $sw = [System.Diagnostics.Stopwatch]::StartNew()
  try {
    $r = Invoke-WebRequest -Uri 'http://localhost:10100/v1/models' -Headers $headers -TimeoutSec 30 -UseBasicParsing
    $sw.Stop()
    $n = (($r.Content | ConvertFrom-Json).data | Measure-Object).Count
    "run $i : HTTP $($r.StatusCode) models=$n 耗时=$($sw.ElapsedMilliseconds)ms"
  } catch {
    $sw.Stop()
    "run $i : ERR 耗时=$($sw.ElapsedMilliseconds)ms : $($_.Exception.Message)"
  }
  Start-Sleep -Milliseconds 500
}
