Set-Location 'D:\Code\Git\loadout'

Write-Output '=== 构建验证实例二进制 ==='
& .\scripts\build-server.ps1 *>&1 | Select-Object -Last 3

Copy-Item 'D:\Code\Git\loadout\bin\loadout.exe' 'C:\Users\Administrator\AppData\Local\Temp\loadout-verify.exe' -Force

Write-Output ''
Write-Output '=== 冷却代理 150s，制造真正的冷启动 ==='
Start-Sleep -Seconds 150

Write-Output ''
Write-Output '=== 冷状态下打一次 /api/unifyai/all（页面初始化走的就是这条） ==='
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3111 2>&1 | Select-String -Pattern 'unifyai/all      ->|opencodex-models ->'
