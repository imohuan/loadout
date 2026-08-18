# Loadout 构建脚本（Windows / PowerShell）
# 用法：powershell -File scripts/build.ps1
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)

Write-Host "==> 构建 Windows 二进制 bin/loadout.exe"
go build -buildvcs=false -o bin/loadout.exe ./apps/server
if ($LASTEXITCODE -ne 0) {
    Write-Error "go build 失败，退出码 $LASTEXITCODE"
    exit $LASTEXITCODE
}

Write-Host "==> 完成：bin/loadout.exe"
