# 打包脚本
# 构建前端(Vite) → 生成图标资源 → 构建 Go

$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot\..

$start = Get-Date
Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  Wails App 打包" -ForegroundColor Cyan
Write-Host "============================================`n" -ForegroundColor Cyan

$config = Get-Content wails.json | ConvertFrom-Json

Write-Host "[1/3] 构建前端 (Vite)..." -ForegroundColor Yellow
Push-Location frontend
npm run build 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL Vite 构建失败" -ForegroundColor Red
    Pop-Location; exit 1
}
Pop-Location
Write-Host "  OK   前端已构建到 frontend/dist/`n" -ForegroundColor Green

$sysoOut = "backend/app/appicon_windows_amd64.syso"
Write-Host "[2/3] 生成图标资源..." -ForegroundColor Yellow
& rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL rsrc 生成 .syso 失败" -ForegroundColor Red
    exit 1
}
Write-Host "  OK   $sysoOut" -ForegroundColor Green

Write-Host "[3/3] 构建 exe..." -ForegroundColor Yellow
$env:CGO_ENABLED = "0"
go build -tags production -ldflags "-w -s -H windowsgui" -o dist/app.exe .
if ($LASTEXITCODE -ne 0) {
    Write-Host "  FAIL 构建失败" -ForegroundColor Red
    exit 1
}

$exe = Get-Item dist/app.exe -ErrorAction Stop
$size = [math]::Round($exe.Length / 1MB, 1)
$elapsed = [math]::Round(((Get-Date) - $start).TotalSeconds, 1)
Write-Host "  OK   dist/app.exe ($size MB)  ($elapsed s)" -ForegroundColor Green

Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  打包完成!  dist/app.exe" -ForegroundColor Green
Write-Host "============================================`n" -ForegroundColor Cyan
Pop-Location
