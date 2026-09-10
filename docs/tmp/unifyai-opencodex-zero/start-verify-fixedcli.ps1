# 用修复版 CLI 源码启动一个验证实例（:3111），证明页面路径能拿到数据。
# 注意：绝不启动/停止 :3000 上用户的服务。
$ErrorActionPreference = 'Continue'

$exe = "$env:TEMP\loadout-verify.exe"
Copy-Item 'D:\Code\Git\loadout\bin\loadout.exe' $exe -Force

$env:LOADOUT_UNIFYAI_CMD = 'node D:\Code\Git\unifyai\src\cli.mjs'
Write-Output "LOADOUT_UNIFYAI_CMD=$env:LOADOUT_UNIFYAI_CMD"

$out = "$env:TEMP\loadout-verify2.log"
$err = "$env:TEMP\loadout-verify2.err"
$p = Start-Process -FilePath $exe -ArgumentList '--port', '3111' -PassThru -RedirectStandardOutput $out -RedirectStandardError $err -WindowStyle Hidden
Write-Output "pid=$($p.Id)"
Start-Sleep -Seconds 8
Write-Output "alive: $(-not $p.HasExited)"
Get-Content $err -ErrorAction SilentlyContinue | Select-Object -Last 5
$p.Id | Out-File "$env:TEMP\loadout-verify2.pid"
