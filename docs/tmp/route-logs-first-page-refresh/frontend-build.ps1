$ErrorActionPreference = 'Continue'
Set-Location 'D:\Code\Git\loadout\frontend'
npm run build 2>&1 | Tee-Object -Variable out
Write-Output "BUILD_EXIT=$LASTEXITCODE"
