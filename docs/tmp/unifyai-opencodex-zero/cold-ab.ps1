Set-Location 'D:\Code\Git\loadout'

Write-Output '=== cooling proxy 150s (both services idle) ==='
Start-Sleep -Seconds 150

Write-Output ''
Write-Output '--- [A] OLD service :3000 (user, pre-fix binary) cold ---'
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3000 2>&1 |
  Select-String -Pattern 'unifyai/all      ->'

Write-Output ''
Write-Output '--- cooling again 150s ---'
Start-Sleep -Seconds 150

Write-Output ''
Write-Output '--- [B] FIXED binary :3111 cold ---'
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3111 2>&1 |
  Select-String -Pattern 'unifyai/all      ->'
