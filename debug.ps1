# Loadout 调试启动脚本：后端 (Go, :5009) + 前端 (Vite 热重载, :5273)
# 启动前自动释放占用端口；Ctrl+C 一次性停止前后端。
param(
    [int]$BackendPort = 5009,
    [int]$FrontendPort = 5273
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

# UTF-8 代码页：避免中文日志乱码（同 启动web2api.ps1 的 chcp 65001）。
cmd /c "chcp 65001 >nul" 2>$null | Out-Null

$backendProc = $null
$frontendProc = $null
$stopping = $false

function Stop-Both {
    if ($script:stopping) { return }
    $script:stopping = $true
    foreach ($p in @($script:frontendProc, $script:backendProc)) {
        if ($p -and -not $p.HasExited) {
            # 进程树整体结束（go run 会派生编译产物子进程）。
            taskkill /PID $p.Id /T /F 2>$null | Out-Null
        }
    }
}

function Free-Port {
    param([int]$Port, [string]$Label)
    $conns = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    if (-not $conns) { return }
    foreach ($c in $conns) {
        $procName = (Get-Process -Id $c.OwningProcess -ErrorAction SilentlyContinue).ProcessName
        Write-Host "[pre] 释放 $Label 端口 $Port（$procName PID=$($c.OwningProcess)）" -ForegroundColor Yellow
        taskkill /PID $c.OwningProcess /T /F 2>$null | Out-Null
    }
    Start-Sleep -Milliseconds 600
}

# Ctrl+C：默认终止脚本进程，PowerShell.Exiting 事件负责清理子进程。

Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action { Stop-Both } | Out-Null

Write-Host ""
Write-Host "===========================================" -ForegroundColor Cyan
Write-Host "  Loadout 调试模式" -ForegroundColor Cyan
Write-Host "===========================================" -ForegroundColor Cyan

# 0) 启动前关闭相关端口（后端 + 前端）。
Free-Port -Port $BackendPort  -Label "后端"
Free-Port -Port $FrontendPort -Label "前端"

# 1) 前端依赖检查
Push-Location "$root/frontend"
if (-not (Test-Path "node_modules/vite")) {
    Write-Host "[1/2] 安装前端依赖..." -ForegroundColor Yellow
    npm install
    if ($LASTEXITCODE -ne 0) { Write-Host "npm install 失败" -ForegroundColor Red; Pop-Location; exit 1 }
} else {
    Write-Host "[1/2] 前端依赖就绪" -ForegroundColor Green
}
Pop-Location

# 2) 启动后端（端口经 LOADOUT_SERVER_ADDR 注入；vite proxy 读同一端口）。
Write-Host "[2/2] 启动后端 :$BackendPort + 前端 :$FrontendPort ..." -ForegroundColor Yellow
$env:LOADOUT_SERVER_ADDR = ":$BackendPort"
$env:VITE_BACKEND_PORT  = "$BackendPort"

$backendProc = Start-Process -FilePath "go" -ArgumentList "run", "./apps/server" `
    -WorkingDirectory $root -PassThru -NoNewWindow

# 等后端端口就绪（最多 30s）。
$ready = $false
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 500
    if ($backendProc.HasExited) { break }
    if (Get-NetTCPConnection -LocalPort $BackendPort -State Listen -ErrorAction SilentlyContinue) { $ready = $true; break }
}
if (-not $ready) {
    Write-Host "后端启动失败（30s 内未监听 $BackendPort），错误输出见上方 go 日志" -ForegroundColor Red
    Stop-Both
    Read-Host "按回车关闭窗口"
    exit 1
}
Write-Host "      后端就绪 http://127.0.0.1:$BackendPort" -ForegroundColor Green

# 3) 启动前端（日志直出当前终端）。
$frontendProc = Start-Process -FilePath "cmd" `
    -ArgumentList "/c", "npx vite --port $FrontendPort --strictPort" `
    -WorkingDirectory "$root/frontend" -PassThru -NoNewWindow

Start-Sleep -Seconds 2
Write-Host ""
Write-Host "===========================================" -ForegroundColor Green
Write-Host "  已启动" -ForegroundColor Green
Write-Host "  前端:  http://localhost:$FrontendPort  (调试入口)" -ForegroundColor Green
Write-Host "  后端:  http://127.0.0.1:$BackendPort   (API / 模型接口)" -ForegroundColor Green
Write-Host "  Ctrl+C 停止全部" -ForegroundColor Yellow
Write-Host "===========================================" -ForegroundColor Green

# 守护循环：任一进程退出即整体收尾。
try {
    while ($true) {
        Start-Sleep -Seconds 1
        if ($backendProc.HasExited) {
            Write-Host ""
            Write-Host "[!] 后端已退出" -ForegroundColor Red
            break
        }
        if ($frontendProc.HasExited) {
            Write-Host ""
            Write-Host "[!] 前端已退出（vite 停止或端口冲突）" -ForegroundColor Red
            break
        }
    }
} finally {
    Stop-Both
    Write-Host ""
    Write-Host "已停止全部进程" -ForegroundColor Cyan
    Read-Host "按回车关闭窗口"
}
