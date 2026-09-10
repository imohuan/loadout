Set-Location 'D:\Code\Git\loadout'

Write-Output '=== start verify instance :3111 (new binary, uses global unifyai) ==='
Copy-Item 'D:\Code\Git\loadout\bin\loadout.exe' 'C:\Users\Administrator\AppData\Local\Temp\loadout-verify.exe' -Force

$out = 'C:\Users\Administrator\AppData\Local\Temp\lv.log'
$err = 'C:\Users\Administrator\AppData\Local\Temp\lv.err'
$p = Start-Process -FilePath 'C:\Users\Administrator\AppData\Local\Temp\loadout-verify.exe' `
      -ArgumentList '--port','3111' -PassThru -WindowStyle Hidden `
      -RedirectStandardOutput $out -RedirectStandardError $err
Write-Output "pid=$($p.Id)"
Start-Sleep -Seconds 8
Write-Output "alive: $(-not $p.HasExited)"
$p.Id | Out-File 'C:\Users\Administrator\AppData\Local\Temp\lv.pid'

Write-Output ''
Write-Output '=== cooling proxy 150s ==='
Start-Sleep -Seconds 150

Write-Output ''
Write-Output '=== cold first call: /api/unifyai/all ==='
go run docs/tmp/unifyai-opencodex-zero/verify_run.go http://127.0.0.1:3111 2>&1 |
  Select-String -Pattern 'unifyai/all      ->|opencodex-models ->'

Write-Output ''
Write-Output '=== warm-up log lines ==='
Get-Content $out -ErrorAction SilentlyContinue | Select-String -Pattern 'warm|elapsed_ms' | Select-Object -Last 10
