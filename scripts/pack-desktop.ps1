# Loadout Desktop 打包脚本
# 使用 web/ 作为前端源码，内嵌 Loadout Server，打包成单个 exe

$ErrorActionPreference = "Stop"
$rootDir = Split-Path $PSScriptRoot -Parent

$start = Get-Date
Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  Loadout Desktop 打包" -ForegroundColor Cyan
Write-Host "============================================`n" -ForegroundColor Cyan

# [1/5] 更新 Desktop 依赖
Write-Host "[1/5] 更新 Desktop 依赖..." -ForegroundColor Yellow
Push-Location "$rootDir\apps\desktop"
go mod tidy 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL go mod tidy 失败" -ForegroundColor Red
    Pop-Location
    exit 1
}
Pop-Location
Write-Host "  OK   依赖已更新`n" -ForegroundColor Green

# [2/5] 构建前端
Write-Host "[2/5] 构建前端 (web/)..." -ForegroundColor Yellow
Push-Location "$rootDir\web"
npm run build 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL 前端构建失败" -ForegroundColor Red
    Pop-Location
    exit 1
}
Pop-Location
Write-Host "  OK   前端已构建到 web/dist/`n" -ForegroundColor Green

# [3/5] 复制产物到 desktop/frontend/dist
Write-Host "[3/5] 复制产物到 desktop..." -ForegroundColor Yellow
$targetDir = "$rootDir\apps\desktop\frontend\dist"
if (Test-Path $targetDir) {
    Remove-Item $targetDir -Recurse -Force
}
Copy-Item -Path "$rootDir\web\dist" -Destination $targetDir -Recurse
Write-Host "  OK   已复制到 apps/desktop/frontend/dist/`n" -ForegroundColor Green

# [4/5] 生成图标资源
Write-Host "[4/5] 生成图标资源..." -ForegroundColor Yellow
Push-Location "$rootDir\apps\desktop"
$config = Get-Content wails.json | ConvertFrom-Json
$sysoOut = "backend/app/appicon_windows_amd64.syso"

& rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  WARN rsrc 未找到或失败，跳过图标嵌入" -ForegroundColor Yellow
} else {
    Write-Host "  OK   $sysoOut" -ForegroundColor Green
}

# [5/5] 构建 exe（内嵌 Loadout Server）
Write-Host "[5/5] 构建 exe..." -ForegroundColor Yellow
$env:CGO_ENABLED = "0"
go build -tags production -ldflags "-w -s -H windowsgui" -o dist/loadout-desktop.exe .
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL 构建失败" -ForegroundColor Red
    Pop-Location
    exit 1
}

$exe = Get-Item dist/loadout-desktop.exe -ErrorAction Stop
$size = [math]::Round($exe.Length / 1MB, 1)
$elapsed = [math]::Round(((Get-Date) - $start).TotalSeconds, 1)
Write-Host "  OK   dist/loadout-desktop.exe ($size MB)  ($elapsed s)" -ForegroundColor Green

Pop-Location

Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  打包完成!  apps/desktop/dist/loadout-desktop.exe" -ForegroundColor Green
Write-Host "  Loadout Server 已内嵌，无需额外启动" -ForegroundColor Green
Write-Host "============================================`n" -ForegroundColor Cyan
