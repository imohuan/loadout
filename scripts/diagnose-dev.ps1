# 诊断开发环境
$ErrorActionPreference = "Continue"

Write-Host "`n=== Loadout Desktop 开发环境诊断 ===`n" -ForegroundColor Cyan

# 1. 检查 Vite 端口
Write-Host "[1] 检查 Vite (端口 9245)..." -ForegroundColor Yellow
$viteTest = Test-NetConnection -ComputerName localhost -Port 9245 -InformationLevel Quiet -WarningAction SilentlyContinue
if ($viteTest) {
    Write-Host "  ✓ Vite 正在运行" -ForegroundColor Green
    try {
        $response = Invoke-WebRequest -Uri "http://localhost:9245" -TimeoutSec 3 -UseBasicParsing
        Write-Host "  ✓ Vite 响应正常 (HTTP $($response.StatusCode))" -ForegroundColor Green
    } catch {
        Write-Host "  ✗ Vite 无法访问: $_" -ForegroundColor Red
    }
} else {
    Write-Host "  ✗ Vite 未运行" -ForegroundColor Red
}

# 2. 检查 Desktop 进程
Write-Host "`n[2] 检查 Desktop 进程..." -ForegroundColor Yellow
$desktopProc = Get-Process | Where-Object { $_.ProcessName -match "loadout-desktop" }
if ($desktopProc) {
    Write-Host "  ✓ 找到进程: $($desktopProc.ProcessName) (PID: $($desktopProc.Id))" -ForegroundColor Green
} else {
    Write-Host "  ✗ Desktop 未运行" -ForegroundColor Red
}

# 3. 检查构建产物
Write-Host "`n[3] 检查构建产物..." -ForegroundColor Yellow
$exePath = "$PSScriptRoot\..\apps\desktop\dist\loadout-desktop-dev.exe"
if (Test-Path $exePath) {
    $fileInfo = Get-Item $exePath
    Write-Host "  ✓ 找到 exe: $($fileInfo.Length) bytes" -ForegroundColor Green
    Write-Host "    最后修改: $($fileInfo.LastWriteTime)" -ForegroundColor Gray
} else {
    Write-Host "  ✗ 找不到 exe: $exePath" -ForegroundColor Red
}

# 4. 检查前端构建产物
Write-Host "`n[4] 检查前端构建产物..." -ForegroundColor Yellow
$frontendDist = "$PSScriptRoot\..\apps\desktop\frontend\dist"
if (Test-Path $frontendDist) {
    $files = Get-ChildItem $frontendDist -Recurse -File
    Write-Host "  ✓ frontend/dist 存在 ($($files.Count) 个文件)" -ForegroundColor Green
} else {
    Write-Host "  ℹ frontend/dist 不存在（开发模式正常）" -ForegroundColor Gray
}

Write-Host "`n=== 诊断完成 ===`n" -ForegroundColor Cyan
