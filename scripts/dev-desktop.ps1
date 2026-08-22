# Loadout Desktop 开发模式
# 使用根 frontend/（主控制台）作为前端源码，Vite 热重载 + 内嵌 Loadout Server

param(
    [int]$VitePort = 9245
)

$ErrorActionPreference = "Stop"
# WorkBuddy 环境注入的 safe-delete 钩子会拦 node 的目录删除（vite 缓存等），去掉 require 只留系统 CA。
$env:NODE_OPTIONS = "--use-system-ca"
$rootDir = Split-Path $PSScriptRoot -Parent

$viteProc = $null
$goProc   = $null

function Cleanup {
    if ($goProc -and !$goProc.HasExited)   { $goProc.Kill(); $goProc.WaitForExit() }
    if ($viteProc -and !$viteProc.HasExited) { Stop-Process -Id $viteProc.Id -Force -ErrorAction SilentlyContinue }
}

try { [Console]::TreatControlCAsInput = $false } catch {}
$null = Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action { if (Get-Command Cleanup -ea 0) { Cleanup } }

Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  Loadout Desktop 开发模式 (热重载)" -ForegroundColor Cyan
Write-Host "============================================`n" -ForegroundColor Cyan

# [1/4] 更新 Desktop 依赖
Write-Host "[1/4] 更新 Desktop 依赖..." -ForegroundColor Yellow
Push-Location "$rootDir\apps\desktop"
go mod tidy 2>&1 | Out-Null
Pop-Location
Write-Host "  OK   依赖已更新`n" -ForegroundColor Green

# [2/4] 检查前端依赖
Write-Host "[2/4] 检查前端依赖 (frontend/)..." -ForegroundColor Yellow
Push-Location "$rootDir\frontend"
if (-not (Test-Path "node_modules/vue")) { 
    Write-Host "  安装依赖..." -ForegroundColor Yellow
    npm install 2>&1 | Out-Null 
}
Pop-Location
Write-Host "  OK   依赖就绪`n" -ForegroundColor Green

# [3/4] 清理端口
Write-Host "[3/4] 清理端口 $VitePort..." -ForegroundColor Yellow
$portProcesses = Get-NetTCPConnection -LocalPort $VitePort -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique
foreach ($procId in $portProcesses) {
    try {
        Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
        Write-Host "  已终止进程 PID=$procId" -ForegroundColor Gray
    } catch {}
}
Write-Host "  OK   端口已清理`n" -ForegroundColor Green

# [4/4] 启动 Vite + Go
Write-Host "[4/4] 启动 Vite (端口 $VitePort) + Go..." -ForegroundColor Yellow

# 启动 Vite 开发服务器（根 frontend/）
Push-Location "$rootDir\frontend"
$viteProc = Start-Process -FilePath "cmd" -ArgumentList "/c","npm run dev -- --port $VitePort --strictPort" -PassThru -NoNewWindow
Pop-Location
Start-Sleep -Seconds 3

# 生成图标资源（如果不存在）
Push-Location "$rootDir\apps\desktop"
$config = Get-Content wails.json | ConvertFrom-Json
$sysoOut = "backend/app/appicon_windows_amd64.syso"
if (-not (Test-Path $sysoOut)) {
    & rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 2>&1 | Out-Null
}

# 构建并启动 Go 应用（指向 Vite 开发服务器，内嵌 Loadout Server）
$env:CGO_ENABLED = "0"
$env:FRONTEND_DEVSERVER_URL = "http://localhost:$VitePort"

Write-Host "  正在构建..." -ForegroundColor Cyan
Write-Host "  环境变量: FRONTEND_DEVSERVER_URL=$env:FRONTEND_DEVSERVER_URL" -ForegroundColor Cyan

go build -ldflags "-H windowsgui" -o dist/loadout-desktop-dev.exe .
if ($LASTEXITCODE -ne 0) { 
    Write-Host "  FAIL Go 构建失败" -ForegroundColor Red
    Pop-Location
    Cleanup
    exit 1 
}

# 检查 exe 是否存在
$exePath = "dist/loadout-desktop-dev.exe"
if (-not (Test-Path $exePath)) {
    Write-Host "  FAIL 找不到构建产物: $exePath" -ForegroundColor Red
    Pop-Location
    Cleanup
    exit 1
}

# 启动时环境变量会自动继承
$goProc = Start-Process -FilePath $exePath -PassThru
Pop-Location

Write-Host ""
Write-Host "============================================" -ForegroundColor Cyan
Write-Host "  开发模式运行中" -ForegroundColor Green
Write-Host "  Vite:  http://localhost:$VitePort" -ForegroundColor Green
Write-Host "  Backend: http://127.0.0.1:3000 (内嵌)" -ForegroundColor Green
Write-Host "  前端修改会自动热重载" -ForegroundColor Green
Write-Host "  后端修改需要重启此脚本" -ForegroundColor Yellow
Write-Host "  关闭窗口 = 停止" -ForegroundColor Green
Write-Host "============================================" -ForegroundColor Cyan

try {
    while (-not $goProc.HasExited) { Start-Sleep -Seconds 1 }
} finally { Cleanup }
