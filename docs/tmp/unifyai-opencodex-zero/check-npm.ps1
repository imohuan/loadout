Set-Location $env:TEMP
Write-Output '--- npm registry version ---'
npm view unifyai version 2>&1 | Select-Object -First 5

Write-Output '--- npm dist tarball ---'
npm view unifyai dist.tarball 2>&1 | Select-Object -First 3

Write-Output '--- local repo copy of config-loader timeout ---'
Select-String -Path 'D:\Code\Git\unifyai\src\core\config-loader.mjs' -Pattern 'PROXY_TIMEOUT_MS =' | Select-Object -First 3
