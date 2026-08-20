# Loadout 打包脚本
# 包含 server 和 desktop 两个目标

param(
    [Parameter(Position=0)]
    [ValidateSet("server", "desktop", "all")]
    [string]$Target = "all"
)

$ErrorActionPreference = "Stop"
$rootDir = Split-Path $PSScriptRoot -Parent

function Build-Server {
    Write-Host "`n==> 构建 Loadout Server" -ForegroundColor Cyan
    Push-Location "$rootDir\apps\server"
    go build -o ..\..\bin\loadout.exe .
    if ($LASTEXITCODE -ne 0) {
        Pop-Location
        throw "Server 构建失败"
    }
    Pop-Location
    Write-Host "  OK   bin/loadout.exe" -ForegroundColor Green
}

function Build-Desktop {
    Write-Host "`n==> 构建 Loadout Desktop" -ForegroundColor Cyan
    & "$PSScriptRoot\pack-desktop.ps1"
    if ($LASTEXITCODE -ne 0) {
        throw "Desktop 构建失败"
    }
}

try {
    switch ($Target) {
        "server" { Build-Server }
        "desktop" { Build-Desktop }
        "all" {
            Build-Server
            Build-Desktop
        }
    }
    
    Write-Host "`n==> 全部完成!" -ForegroundColor Green
} catch {
    Write-Host "`n==> 构建失败: $_" -ForegroundColor Red
    exit 1
}
