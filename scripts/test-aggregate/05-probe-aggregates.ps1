# 05-probe-aggregates.ps1
# 调 /api/aggregates 看 auto-demo 的 enabled 字段。
# 关键诊断：如果 enabled=false 而 /v1/models 又找不到它，根因就是「前端没传 enabled 字段」。

. "$PSScriptRoot\_common.ps1"

Write-Head "05 查看 /api/aggregates 里 auto-demo 的 enabled 字段"

if (-not (Test-Path $script:CookieFile)) { throw "请先运行 02-login.ps1" }

$resp = Invoke-AdminApi -Method GET -Path '/api/aggregates'
Save-Report '05-aggregates' @{ data=$resp }

$auto = @($resp | Where-Object { $_.name -eq 'auto-demo' })[0]
if (-not $auto) {
    Write-Err "没找到 auto-demo（先跑 03-seed-test-data.ps1）"
    exit 1
}

Write-Info "auto-demo 完整记录："
$auto | ConvertTo-Json -Depth 6 | Write-Host

$enabled = $auto.enabled
Write-Info "enabled 字段 = $enabled"

# 关联 /v1/models 结果
$res4Path = Join-Path $script:ReportDir '04-result.json'
if (Test-Path $res4Path) {
    $res4 = Get-Content $res4Path -Raw | ConvertFrom-Json
    $hasInV1 = $res4.has_auto_demo
    Write-Info "04 步结果：/v1/models 包含 auto-demo = $hasInV1"
    if (-not $enabled -and -not $hasInV1) {
        Write-Err "❗ 根因定位：DB 里 enabled=$enabled → model-gateway 在 aggregateNames 里被 `if a.Enabled` 过滤掉"
        Write-Err "   修复：前端 AggregateEditor.vue 的 submit() 现在已带上 enabled："
        Write-Err "     emit('save', { name, enabled: form.enabled, targets })"
        Write-Err "   历史原因：修复前前端只发 {name, targets}，Go 零值 enabled=false 写入 DB"
    } elseif ($enabled -and $hasInV1) {
        Write-Ok "✅ enabled=$enabled 且 /v1/models 含 auto-demo，链路正常"
    } elseif ($enabled -and -not $hasInV1) {
        Write-Warn "DB 端 enabled=$enabled 但 /v1/models 仍找不到 auto-demo，疑似其他过滤（key 白名单 / channel 探测等）"
    } else {
        Write-Warn "DB 端 enabled=$enabled=false 但 /v1/models 仍包含 auto-demo，配置可能未刷新（重启服务？）"
    }
}
