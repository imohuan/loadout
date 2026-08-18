# 02-login.ps1
# 读临时目录里首启生成的管理员密码，POST /api/login 拿 session cookie。

. "$PSScriptRoot\_common.ps1"

Write-Head "02 登录后台拿 session"

if (-not (Test-Path $script:BinPath)) { throw "请先运行 01-build-and-start.ps1" }

$password = Read-AdminPassword
Write-Info "用户名 admin，密码长度 $($password.Length)"

$resp = Invoke-WebRequest -Uri "$($script:BaseUrl)/api/login" `
    -Method POST `
    -ContentType 'application/json' `
    -Body (ConvertTo-Json -Depth 5 @{ username='admin'; password=$password }) `
    -SkipHttpErrorCheck

if ($resp.StatusCode -ne 200) {
    Write-Err "登录失败：$($resp.StatusCode) $($resp.Content)"
    Save-Report '02-login' @{ status=$resp.StatusCode; body=$resp.Content }
    exit 1
}

# 保存 cookie（Set-Cookie 头）
$cookieHeader = $resp.Headers['Set-Cookie'] | Select-Object -First 1
if (-not $cookieHeader) {
    Write-Err "响应没有 Set-Cookie 头"
    exit 1
}
# 去掉 Path/Expires 等，只保留 name=value
$cookieValue = ($cookieHeader -split ';')[0].Trim()
Set-Content -Path $script:CookieFile -Value $cookieValue -Encoding UTF8
Write-Ok "登录成功，cookie=$cookieValue"

# 验证 session：访问 /api/channels
$verify = Invoke-AdminApi -Method GET -Path '/api/channels'
Save-Report '02-login' @{ status=200; channels_count=($verify | Measure-Object).Count }

Write-Ok "session 可用，channels 数=$($verify.Count)（不报错即通过）"
Write-Info "下一步：运行 03-seed-test-data.ps1 准备测试数据"
