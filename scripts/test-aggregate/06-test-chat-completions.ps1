# 06-test-chat-completions.ps1
# 端到端验证 /v1/chat/completions 对虚拟模型的处理：
#   - 不限制 key + model=auto-demo  → 不应被 403（白名单通过；可能因上游不可达 5xx，这是预期）
#   - 限制 key（models=[real-model]）+ model=auto-demo → 应 403（白名单拦截虚拟模型）

. "$PSScriptRoot\_common.ps1"

Write-Head "06 测试 /v1/chat/completions 对虚拟模型的白名单行为"

$seedPath = Join-Path $script:ReportDir '03-seeded.json'
if (-not (Test-Path $seedPath)) { throw "请先运行 03-seed-test-data.ps1" }
$seed = Get-Content $seedPath -Raw | ConvertFrom-Json

$body = @{
    model    = 'auto-demo'
    messages = @(@{ role='user'; content='ping' })
    stream   = $false
}

# 1) 不限制 key → 不应 403
Write-Info "用不限制 key 调 chat model=auto-demo（期望：通过白名单）"
$r1 = Invoke-ModelApi -Method POST -Path '/v1/chat/completions' -ApiKey $seed.api_key_unrestricted -Body $body
$status1 = $r1.StatusCode
$bodyText1 = if ($r1.Content.Length -gt 200) { $r1.Content.Substring(0,200) + '...' } else { $r1.Content }
Write-Info "HTTP $status1：$bodyText1"
Save-Report '06-chat-unrestricted' @{ status=$status1; body=$r1.Content }

$pass1 = $true
if ($status1 -eq 403) {
    Write-Err "❌ 不限制 key 仍被 403 拒绝，说明白名单逻辑有 bug"
    $pass1 = $false
} elseif ($status1 -eq 200) {
    Write-Ok "✅ 200（虚拟模型在不限制 key 下走通）"
} else {
    Write-Warn "返回 $status1（非 200/403；通常是上游不可达，符合预期）"
}

# 2) 限制 key + model=auto-demo → 应 403
Write-Info "用限制 key（models=[real-model]）调 chat model=auto-demo（期望：403）"
$r2 = Invoke-ModelApi -Method POST -Path '/v1/chat/completions' -ApiKey $seed.api_key_restricted -Body $body
$status2 = $r2.StatusCode
$bodyText2 = if ($r2.Content.Length -gt 200) { $r2.Content.Substring(0,200) + '...' } else { $r2.Content }
Write-Info "HTTP $status2：$bodyText2"
Save-Report '06-chat-restricted' @{ status=$status2; body=$r2.Content }

$pass2 = $true
if ($status2 -eq 403) {
    Write-Ok "✅ 403（受限 key 正确拦截虚拟模型）"
} else {
    Write-Err "❌ 受限 key 未拦截虚拟模型（status=$status2）"
    $pass2 = $false
}

if ($pass1 -and $pass2) { exit 0 } else { exit 2 }
