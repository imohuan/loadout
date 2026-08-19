# 03-seed-test-data.ps1
# 准备最小可复现数据（幂等：先清理旧的 channel/aggregate/key，再重建）：
#   - 1 个 channel（不可达地址占位）
#   - 1 个聚合模型 auto-demo，enabled=true
#   - 1 个 sk-key（models=空，不限制；复现用户场景）
#   - 1 个 sk-key（models=[real-model]，验证受限 key 拦截虚拟模型）

. "$PSScriptRoot\_common.ps1"

Write-Head "03 灌入测试数据（幂等）"

if (-not (Test-Path $script:CookieFile)) { throw "请先运行 02-login.ps1" }

$hdr = @{ Cookie = (Get-Content $script:CookieFile -Raw).Trim(); 'Content-Type' = 'application/json' }
$jsonPost = $InvokeWebRequestSplat = @{}

# ---- 清理旧数据 ----
Write-Info "清理旧 channels"
try {
    $chs = Invoke-RestMethod -Uri "$($script:BaseUrl)/api/channels" -Headers $hdr -Method GET
    foreach ($c in @($chs)) {
        if ($c.id) { Invoke-WebRequest -Uri "$($script:BaseUrl)/api/channels/$($c.id)" -Headers $hdr -Method DELETE -SkipHttpErrorCheck | Out-Null }
    }
} catch {}
Write-Info "清空旧 aggregates（PUT []）"
Invoke-WebRequest -Uri "$($script:BaseUrl)/api/aggregates" -Headers $hdr -Method PUT -Body '[]' -SkipHttpErrorCheck | Out-Null
Write-Info "清理旧 sk-keys"
try {
    $ks = Invoke-RestMethod -Uri "$($script:BaseUrl)/api/keys" -Headers $hdr -Method GET
    foreach ($k in @($ks.sk_keys)) {
        if ($k.id) { Invoke-WebRequest -Uri "$($script:BaseUrl)/api/keys/sk/$($k.id)" -Headers $hdr -Method DELETE -SkipHttpErrorCheck | Out-Null }
    }
} catch {}

# ---- 1) 创建 channel ----
Write-Info "创建 channel（不可达地址，仅占位）"
$chBody = @{
    name           = 'fake'
    base_url       = 'http://127.0.0.1:19999/v1'
    api_key        = 'sk-fake-placeholder'
    manual_enabled = $true
} | ConvertTo-Json -Depth 5
$chCreateResp = Invoke-WebRequest -Uri "$($script:BaseUrl)/api/channels" -Headers $hdr -Method POST -Body $chBody -SkipHttpErrorCheck
if ($chCreateResp.StatusCode -ne 200) {
    Write-Err "创建 channel 失败：$($chCreateResp.StatusCode) $($chCreateResp.Content)"
    exit 1
}
$chCreated = $chCreateResp.Content | ConvertFrom-Json
$channelId = $chCreated.id
Write-Ok "channel 已创建：id=$channelId"

# ---- 2) 创建聚合模型 auto-demo（用真实 channelId）----
Write-Info "创建聚合模型 auto-demo（enabled=true，2 targets，channel_id=$channelId）"
$aggJson = "[{""name"":""auto-demo"",""enabled"":true,""targets"":[{""model"":""claude-haiku-4-5-20251001"",""channel_id"":""$channelId""},{""model"":""claude-haiku-4-5-20251001"",""channel_id"":""$channelId""}]}]"
$aggResp = Invoke-WebRequest -Uri "$($script:BaseUrl)/api/aggregates" -Headers $hdr -Method PUT -Body $aggJson -SkipHttpErrorCheck
if ($aggResp.StatusCode -ne 200) {
    Write-Err "PUT /api/aggregates 失败：$($aggResp.StatusCode) $($aggResp.Content)"
    Save-Report '03-error' @{ step='aggregates'; status=$aggResp.StatusCode; body=$aggResp.Content }
    exit 1
}
Write-Ok "聚合模型 auto-demo 已就位"

# ---- 3) 不限制 sk-key ----
Write-Info "创建 sk-key（models=空，不限制）"
$keyBody = @{ name='test-unrestricted'; models=@() } | ConvertTo-Json -Depth 5
$keyResp = Invoke-RestMethod -Uri "$($script:BaseUrl)/api/keys/sk" -Headers $hdr -Method POST -Body $keyBody
$apiKey = $keyResp.key
Write-Ok "sk-key 前 12 位：$($apiKey.Substring(0, [Math]::Min(12, $apiKey.Length)))…"

# ---- 4) 限制 sk-key ----
Write-Info "创建 sk-key（models=[real-model]）"
$keyBody2 = @{ name='test-restricted'; models=@('real-model') } | ConvertTo-Json -Depth 5
$keyResp2 = Invoke-RestMethod -Uri "$($script:BaseUrl)/api/keys/sk" -Headers $hdr -Method POST -Body $keyBody2
$restrictedKey = $keyResp2.key
Write-Ok "受限 sk-key 前 12 位：$($restrictedKey.Substring(0, [Math]::Min(12, $restrictedKey.Length)))…"

Save-Report '03-seeded' @{
    api_key_unrestricted = $apiKey
    api_key_restricted   = $restrictedKey
    aggregate_name       = 'auto-demo'
    aggregate_enabled    = $true
    channel_id           = $channelId
}

Write-Info "数据已就绪。下一步：04-probe-v1-models.ps1 验证 /v1/models"
