Write-Output '--- verify2.log ---'
if (Test-Path "$env:TEMP\loadout-verify2.log") {
  Get-Content "$env:TEMP\loadout-verify2.log" | Select-String -Pattern 'unifyai' | Select-Object -Last 25
} else { Write-Output 'no log' }

Write-Output '--- verify2.err ---'
if (Test-Path "$env:TEMP\loadout-verify2.err") {
  Get-Content "$env:TEMP\loadout-verify2.err" | Select-Object -Last 30
} else { Write-Output 'no err' }
