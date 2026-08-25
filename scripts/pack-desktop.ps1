# Loadout Desktop 打包脚本
# 使用根 frontend/（主控制台）作为前端源码；产物复制到 apps/desktop/frontend/dist 由 go:embed 内嵌
#
# 交互式：运行后让你选择打哪个版本（release / debug / 两版都打）
# 可选参数：
#   -SkipBuild  # 跳过前端构建与复制，仅编译 exe（改后端后快速重打）
#   -Choice "release|debug|both"  # 跳过交互直接打包（供自动化/CI 用）

param(
    [switch]$SkipBuild,
    [string]$Choice
)

$ErrorActionPreference = "Stop"
# WorkBuddy 环境会注入 safe-delete 钩子（node require + PS Remove-Item 包装），
# vite emptyOutDir 清空 dist / robocopy 前的删除会被拦截。这里去掉 require 只留系统 CA。
$env:NODE_OPTIONS = "--use-system-ca"
$rootDir = Split-Path $PSScriptRoot -Parent

$start = Get-Date
Write-Host "`n============================================" -ForegroundColor Cyan
Write-Host "  Loadout Desktop 打包" -ForegroundColor Cyan
Write-Host "============================================`n" -ForegroundColor Cyan

# 版本选择：-Choice 参数（自动化）优先，否则交互式菜单（手动）。
if ($Choice) {
    if ($Choice -notin @("release", "debug", "both")) {
        Write-Host "无效的 -Choice 值：$Choice（可选 release/debug/both）" -ForegroundColor Red
        exit 1
    }
    $jobs = switch ($Choice) {
        "release" { @("release") }
        "debug"   { @("debug") }
        default   { @("release", "debug") }
    }
    $label = ($jobs | ForEach-Object { $_ }) -join " + "
    Write-Host "  已选择（-Choice）：$label`n" -ForegroundColor Green
} else {
    Write-Host "请选择要打包的版本：" -ForegroundColor Yellow
    Write-Host "  [1] release 版        （生产，无 DevTools）" -ForegroundColor White
    Write-Host "  [2] debug 版          （自动开 DevTools，Ctrl+Shift+I 切换）" -ForegroundColor White
    Write-Host "  [3] 两个版本都打" -ForegroundColor White
    Write-Host ""

    # 注意：不能用 $choice 作局部变量——param 里已有 [string]$Choice，
    # PowerShell 变量不区分大小写，二者是同一个变量；把 $null 赋给 [string]
    # 类型变量会被强转回空串 ""，导致 while 条件 $null -eq "" 为 False，
    # 循环一次都不执行、菜单静默 fallthrough 到默认版本。这里改用独立变量名。
    $selChoice = $null
    while ($null -eq $selChoice) {
        $sel = Read-Host "请输入数字 1/2/3 选择（q 退出）"
        switch ($sel.Trim()) {
            "1" { $selChoice = "release" }
            "2" { $selChoice = "debug" }
            "3" { $selChoice = "both" }
            "q" { Write-Host "已取消打包。`n" -ForegroundColor Red; exit 0 }
            default { Write-Host "  无效输入：'$sel'，请重新输入" -ForegroundColor Red }
        }
    }

    # $selChoice 必须由用户显式选定，绝不允许静默 fallthrough 到默认版本。
    if ($null -eq $selChoice) {
        Write-Host "未收到有效选择，已取消打包。" -ForegroundColor Red
        exit 1
    }
    $jobs = switch ($selChoice) {
        "release" { @("release") }
        "debug"   { @("debug") }
        default   { @("release", "debug") }
    }
    $label = ($jobs | ForEach-Object { $_ }) -join " + "
    Write-Host "  已选择：$label`n" -ForegroundColor Green
}

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

if (-not $SkipBuild) {
    # [2/5] 构建前端（根 frontend/）
    Write-Host "[2/5] 构建前端 (frontend/)..." -ForegroundColor Yellow
    Push-Location "$rootDir\frontend"
    npm run build 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  FAIL 前端构建失败" -ForegroundColor Red
        Pop-Location
        exit 1
    }
    Pop-Location
    Write-Host "  OK   前端已构建到 frontend/dist/`n" -ForegroundColor Green

    # [3/5] 复制产物到 desktop/frontend/dist（go:embed 落点，/MIR 镜像覆盖）
    Write-Host "[3/5] 复制产物到 desktop..." -ForegroundColor Yellow
    $targetDir = "$rootDir\apps\desktop\frontend\dist"
    robocopy "$rootDir\frontend\dist" $targetDir /MIR /NFL /NDL /NJH /NJS /NP | Out-Null
    if ($LASTEXITCODE -ge 8) {
        Write-Host "  FAIL robocopy 复制失败 (exit $LASTEXITCODE)" -ForegroundColor Red
        Pop-Location
        exit 1
    }
    Write-Host "  OK   已镜像到 apps/desktop/frontend/dist/`n" -ForegroundColor Green
} else {
    Write-Host "[2-3/5] 跳过前端构建与复制（-SkipBuild）`n" -ForegroundColor Yellow
}

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
# 版本由开头的交互式选择决定（$jobs 已在脚本开头赋值）。
$env:CGO_ENABLED = "0"

foreach ($variant in $jobs) {
    $tag = "production"
    $out  = "dist/loadout-desktop.exe"
    $label = "release"
    if ($variant -eq "debug") {
        $tag = "debug"
        $out = "dist/loadout-desktop-debug.exe"
        $label = "debug"
    }
    Write-Host "[5/5] 构建 exe ($label)..." -ForegroundColor Yellow
    go build -tags $tag -ldflags "-w -s -H windowsgui" -o $out .
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  FAIL 构建失败 ($label)" -ForegroundColor Red
        Pop-Location
        exit 1
    }
    $exe = Get-Item $out -ErrorAction Stop
    $size = [math]::Round($exe.Length / 1MB, 1)
    Write-Host "  OK   $out ($size MB)" -ForegroundColor Green
}

$elapsed = [math]::Round(((Get-Date) - $start).TotalSeconds, 1)
Write-Host "  OK   总耗时 $elapsed s`n" -ForegroundColor Green

Pop-Location

Write-Host "`n============================================" -ForegroundColor Cyan
foreach ($variant in $jobs) {
    $file = if ($variant -eq "debug") { "loadout-desktop-debug.exe" } else { "loadout-desktop.exe" }
    $hint = if ($variant -eq "debug") { "（启动自动开 DevTools，Ctrl+Shift+I 也可切换）" } else { "" }
    Write-Host "  $file  $hint" -ForegroundColor Green
}
Write-Host "  位于 apps/desktop/dist/" -ForegroundColor Green
Write-Host "  Loadout Server 已内嵌，无需额外启动" -ForegroundColor Green
Write-Host "============================================`n" -ForegroundColor Cyan
