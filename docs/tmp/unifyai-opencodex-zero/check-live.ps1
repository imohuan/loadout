Set-Location 'D:\Code\Git\loadout'

Write-Output '=== 现网服务（3000, pid 10664）真实返回 ==='
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3000 2>&1 | Select-String -Pattern 'unifyai/all|opencodex-models ->|首个模型'
