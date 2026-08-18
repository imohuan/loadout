# 07-stop-and-report.ps1
# 停掉后台服务，汇总各步结果，输出最终报告。
# 不删除 HomeDir/reports：保留供人工排查；如需彻底清理，手动删 $env:TEMP\loadout-test-aggregate。

. "$PSScriptRoot\_common.ps1"

Write-Head "07 停服 + 综合报告"

# 停服
if (Test-Path $script:PidFile) {
    $pidVal = Get-Content $script:PidFile -Raw
    $proc = Get-Process -Id $pidVal -ErrorAction SilentlyContinue
    if ($proc) {
        Write-Info "停止 PID $pidVal ..."
        Stop-Process -Id $pidVal -Force
        Start-Sleep -Seconds 1
    }
    Remove-Item $script:PidFile
}

# 综合报告
Write-Host ""
Write-Host "================ 综合报告 ================" -ForegroundColor Magenta
$steps = @('01-started','02-login','03-seeded','04-result','05-aggregates','06-chat-unrestricted','06-chat-restricted')
foreach ($s in $steps) {
    $p = Join-Path $script:ReportDir "$s.json"
    if (Test-Path $p) {
        Write-Host ""; Write-Host "--- $s ---" -ForegroundColor Cyan
        Get-Content $p -Raw
    } else {
        Write-Warn "未找到 $s"
    }
}

Write-Host ""
Write-Info "日志目录：$($script:HomeDir)"
Write-Info "报告目录：$($script:ReportDir)"
Write-Info "清理命令：Remove-Item -Recurse -Force $($script:HomeDir)"
