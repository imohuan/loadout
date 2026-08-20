# 04-probe-v1-models.ps1
# 核心诊断：用不限制的 sk-key 调 GET /v1/models，看返回里有没有 auto-demo。
# 这是用户报告的"找不到虚拟模型"问题的直接验证点。

. "$PSScriptRoot\_common.ps1"

Write-Head "04 探测 /v1/models 是否含 auto-demo"

$seedPath = Join-Path $script:ReportDir '03-seeded.json'
if (-not (Test-Path $seedPath)) { throw "请先运行 03-seed-test-data.ps1" }
$seed = Get-Content $seedPath -Raw | ConvertFrom-Json

$apiKey = $seed.api_key_unrestricted
$resp = Invoke-ModelApi -Method GET -Path '/v1/models' -ApiKey $apiKey
$status = $resp.StatusCode
$body   = $resp.Content

Write-Info "HTTP $status"
Save-Report '04-models-unrestricted' @{ status=$status; body=$body }

if ($status -ne 200) {
    Write-Err "/v1/models 返回 $status：$body"
    exit 1
}

# 解析模型 ID 列表
$parsed = $body | ConvertFrom-Json
$ids = @($parsed.data | ForEach-Object { $_.id })
Write-Info "返回 $($ids.Count) 个模型：$($ids -join ', ')"
foreach ($id in $ids) { Write-Host "    - $id" }

if ($ids -contains 'auto-demo') {
    Write-Ok "✅ /v1/models 包含虚拟模型 auto-demo"
    Save-Report '04-result' @{ has_auto_demo=$true; model_count=$ids.Count }
    exit 0
} else {
    Write-Err "❌ /v1/models 不含 auto-demo！这就是用户报告的问题"
    Write-Err "   继续运行 05-probe-aggregates.ps1 看 DB 里 enabled 字段"
    Save-Report '04-result' @{ has_auto_demo=$false; model_count=$ids.Count; models=$ids }
    exit 2
}
