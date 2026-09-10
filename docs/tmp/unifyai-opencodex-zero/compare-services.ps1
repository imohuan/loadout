Set-Location 'D:\Code\Git\loadout'

Write-Output '=== FIXED binary on :3111 -- cold-proxy e2e ==='
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3111 2>&1 |
  Select-String -Pattern 'unifyai/all      ->|opencodex-models ->'

Write-Output ''
Write-Output '=== OLD service on :3000 (user, pre-fix binary) ==='
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3000 2>&1 |
  Select-String -Pattern 'unifyai/all      ->|opencodex-models ->'
