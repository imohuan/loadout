$ErrorActionPreference = 'Continue'
$cli = 'C:\Users\Administrator\AppData\Roaming\npm\node_modules\unifyai\src\cli.mjs'
$src = 'C:\Users\Administrator\.opencodex\config.json'
foreach ($i in 1..4) {
  $raw = & node $cli --list models --json --source $src 2>&1 | Out-String
  if ($raw -match '"rawCount":\s*(\d+)') { $rc = $Matches[1] } else { $rc = '?' }
  if ($raw -match '"count":\s*(\d+)') { $c = $Matches[1] } else { $c = '?' }
  if ($raw -match '"degraded":\s*(\w+)') { $d = $Matches[1] } else { $d = '?' }
  "run $i : rawCount=$rc count=$c degraded=$d"
  Start-Sleep -Milliseconds 700
}
