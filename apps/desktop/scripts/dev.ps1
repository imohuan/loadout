# 开发模式 — Vite 热重载 + Go 后端
param(
    [int]$VitePort = 9245
)

$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot\..

$viteProc = $null
$goProc   = $null

function Cleanup {
    if ($goProc -and !$goProc.HasExited)   { Stop-Process -Id $goProc.Id -Force -ErrorAction SilentlyContinue }
    if ($viteProc -and !$viteProc.HasExited) { Stop-Process -Id $viteProc.Id -Force -ErrorAction SilentlyContinue }
    Pop-Location
}

try { [Console]::TreatControlCAsInput = $false } catch {}
$null = Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action { if (Get-Command Cleanup -ea 0) { Cleanup } }

Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  Wails App 开发模式 (热重载)" -ForegroundColor Cyan
Write-Host "============================================`n" -ForegroundColor Cyan

Push-Location frontend
if (-not (Test-Path "node_modules/vue")) { npm install 2>&1 | Out-Null }
npx vite build 2>&1 | Out-Null
Pop-Location
Write-Host "[1/2] 前端就绪`n" -ForegroundColor Green

Write-Host "[2/2] 启动 Vite (端口 $VitePort) + Go..." -ForegroundColor Yellow
$viteProc = Start-Process -FilePath "cmd" -ArgumentList "/c","npx vite --port $VitePort --strictPort" -WorkingDirectory "frontend" -PassThru -NoNewWindow
Start-Sleep -Seconds 3

$config = Get-Content wails.json | ConvertFrom-Json
$sysoOut = "backend/app/appicon_windows_amd64.syso"
if (-not (Test-Path $sysoOut)) {
    & rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 2>&1 | Out-Null
}

$env:CGO_ENABLED = "0"
$env:FRONTEND_DEVSERVER_URL = "http://localhost:$VitePort"

go build -ldflags "-H windowsgui" -o dist/app-dev.exe .
if ($LASTEXITCODE -ne 0) { Write-Host "Go build FAIL" -ForegroundColor Red; Cleanup; exit 1 }

$goProc = Start-Process -FilePath "dist/app-dev.exe" -PassThru

Write-Host ""
Write-Host "============================================" -ForegroundColor Cyan
Write-Host "  开发模式运行中" -ForegroundColor Green
Write-Host "  Vite:  http://localhost:$VitePort" -ForegroundColor Green
Write-Host "  关闭窗口 = 停止" -ForegroundColor Green
Write-Host "============================================" -ForegroundColor Cyan

try {
    while (-not $goProc.HasExited) { Start-Sleep -Seconds 1 }
} finally { Cleanup }
