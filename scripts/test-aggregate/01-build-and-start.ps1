# 01-build-and-start.ps1
# 1) 编译 bin/loadout.exe
# 2) 用临时 LOADOUT_TEST_HOME_DIR 隔离数据目录（不污染 ~/.loadout）
# 3) 后台启动 loadout，等待端口就绪

. "$PSScriptRoot\_common.ps1"

Write-Head "01 build + 后台启动"

# 1) build
if (-not (Test-Path $script:BinPath)) {
    Write-Info "编译 loadout.exe ..."
    & go build -o $script:BinPath "$script:RepoRoot\apps\server"
    if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
} else {
    Write-Info "loadout.exe 已存在，跳过编译（删除 bin\loadout.exe 强制重编）"
}

# 2) 清理旧数据 & 旧进程
if (Test-Path $script:HomeDir) {
    Write-Info "清理旧数据目录 $($script:HomeDir)"
    Remove-Item -Recurse -Force $script:HomeDir
}
New-Item -ItemType Directory -Path $script:HomeDir | Out-Null

# 如果上轮有进程在跑，先停掉
if (Test-Path $script:PidFile) {
    $old = Get-Content $script:PidFile -Raw
    if ($old -and (Get-Process -Id $old -ErrorAction SilentlyContinue)) {
        Write-Warn "停掉残留 PID $old"
        Stop-Process -Id $old -Force
    }
    Remove-Item $script:PidFile
}

# 3) 后台启动
Write-Info "启动 $($script:BinPath)（home=$($script:HomeDir), port=$($script:Port)）"
$env:LOADOUT_HOME_DIR  = $script:HomeDir
$env:LOADOUT_SERVER_ADDR = ":$($script:Port)"
$proc = Start-Process -FilePath $script:BinPath `
                      -WorkingDirectory $script:RepoRoot `
                      -RedirectStandardOutput (Join-Path $script:HomeDir 'stdout.log') `
                      -RedirectStandardError  (Join-Path $script:HomeDir 'stderr.log') `
                      -PassThru -WindowStyle Hidden
Set-Content -Path $script:PidFile -Value $proc.Id
Write-Ok "已启动，PID=$($proc.Id)"

# 4) 等端口
$ok = Wait-PortUp -TimeoutSec 25
if (-not $ok) {
    Write-Err "服务未起来，查看日志：$($script:HomeDir)\loadout.log"
    Get-Content (Join-Path $script:HomeDir 'stderr.log') -ErrorAction SilentlyContinue | Select-Object -Last 30
    exit 1
}

Save-Report '01-started' @{ pid=$proc.Id; home=$script:HomeDir; port=$script:Port }
Write-Ok "下一步：运行 02-login.ps1 登录后台"
