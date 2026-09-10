$ErrorActionPreference = 'Stop'
# 用后端自己的注册表逻辑没法直接调；这里改成直接跑一段 Go 集成验证：
# 启动新 exe 的副本在空闲端口，用 --help/health 探测是否起来，然后打 API。
$exe = 'D:\Code\Git\loadout\bin\loadout.exe'
$new = Join-Path $env:TEMP 'loadout-verify.exe'
Copy-Item -LiteralPath $exe -Destination $new -Force
"exe copied: $new"
