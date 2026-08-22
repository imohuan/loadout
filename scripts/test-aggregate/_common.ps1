# scripts/test-aggregate/_common.ps1
# 7 个测试脚本共用的辅助函数与配置。临时数据目录，避免污染 ~/.loadout。

$ErrorActionPreference = 'Stop'

# --- 路径与端口（可被环境变量覆盖） ---
$script:RepoRoot    = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$script:HomeDir     = if ($env:LOADOUT_TEST_HOME_DIR) { $env:LOADOUT_TEST_HOME_DIR } else { Join-Path $env:TEMP 'loadout-test-aggregate' }
$script:Port        = if ($env:LOADOUT_TEST_PORT)     { [int]$env:LOADOUT_TEST_PORT }   else { 3100 }
$script:BaseUrl     = "http://127.0.0.1:$($script:Port)"
$script:BinPath     = Join-Path $script:RepoRoot 'bin\loadout.exe'
$script:PidFile     = Join-Path $script:HomeDir 'loadout.pid'
$script:CookieFile  = Join-Path $script:HomeDir 'admin-cookie.txt'
$script:PasswordFile= Join-Path $script:HomeDir 'admin-password'
$script:ReportDir   = Join-Path $PSScriptRoot 'reports'
$script:LogFile     = Join-Path $script:HomeDir 'loadout.log'

if (-not (Test-Path $script:ReportDir)) { New-Item -ItemType Directory -Path $script:ReportDir | Out-Null }

# --- 颜色输出 ---
function Write-Info  ([string]$m) { Write-Host "[INFO] $m" -ForegroundColor Cyan }
function Write-Ok    ([string]$m) { Write-Host "[ OK ] $m" -ForegroundColor Green }
function Write-Warn  ([string]$m) { Write-Host "[WARN] $m" -ForegroundColor Yellow }
function Write-Err   ([string]$m) { Write-Host "[FAIL] $m" -ForegroundColor Red }
function Write-Head  ([string]$m) { Write-Host ""; Write-Host "=== $m ===" -ForegroundColor Magenta }

# --- 报告输出（每步结果落到 reports/<step>.json） ---
function Save-Report {
    param([string]$Name, [hashtable]$Data)
    $path = Join-Path $script:ReportDir "$Name.json"
    $Data | ConvertTo-Json -Depth 10 | Set-Content -Path $path -Encoding UTF8
}

# --- HTTP 助手：管理端（带 session cookie）---
function Invoke-AdminApi {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Path,
        [object]$Body
    )
    $url = "$($script:BaseUrl)$Path"
    $headers = @{ 'Content-Type' = 'application/json' }
    if (Test-Path $script:CookieFile) {
        $cookieRaw = (Get-Content $script:CookieFile -Raw).Trim()
        if ($cookieRaw) { $headers['Cookie'] = $cookieRaw }
    }
    $params = @{
        Method = $Method
        Uri    = $url
        Headers= $headers
    }
    if ($PSBoundParameters.ContainsKey('Body') -and $null -ne $Body) {
        $params.Body = ($Body | ConvertTo-Json -Depth 10)
    }
    return Invoke-RestMethod @params
}

# --- HTTP 助手：模型端（Bearer sk-key）---
function Invoke-ModelApi {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Path,
        [string]$ApiKey,
        [object]$Body
    )
    $url = "$($script:BaseUrl)$Path"
    $headers = @{ 'Content-Type' = 'application/json' }
    if ($ApiKey) { $headers['Authorization'] = "Bearer $ApiKey" }
    $params = @{
        Method = $Method
        Uri    = $url
        Headers= $headers
    }
    if ($PSBoundParameters.ContainsKey('Body') -and $null -ne $Body) {
        $params.Body = ($Body | ConvertTo-Json -Depth 10)
    }
    # 用 HttpClient 直接拿 status + body（Invoke-RestMethod 400/403/500 会抛错）
    return Invoke-WebRequest @params -SkipHttpErrorCheck
}

# --- 等待端口可连（最多 N 秒）---
function Wait-PortUp {
    param([int]$TimeoutSec = 20)
    Write-Info "等待 $($script:BaseUrl) 起来（最多 $TimeoutSec 秒）..."
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            Invoke-WebRequest -Uri "$($script:BaseUrl)/" -UseBasicParsing -TimeoutSec 2 | Out-Null
            Write-Ok "服务已启动"
            return $true
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    Write-Err "等待超时"
    return $false
}

# --- 读取 admin 密码（首启随机写入）---
function Read-AdminPassword {
    if (-not (Test-Path $script:PasswordFile)) {
        throw "未找到管理员密码文件 $($script:PasswordFile)；请先运行 01-build-and-start.ps1"
    }
    return (Get-Content $script:PasswordFile -Raw).Trim()
}
