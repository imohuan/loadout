$ErrorActionPreference = 'Continue'
Set-Location 'D:\Code\Git\loadout\frontend'
npm run build 2>&1 | Select-Object -Last 6
Write-Output "BUILD_EXIT=$LASTEXITCODE"
npx prettier --check src/views/RouteLogsView.vue src/components/route-logs/RouteLogFilters.vue src/lib/autoRefresh.ts 2>&1 | Select-Object -Last 10
Write-Output "PRETTIER_EXIT=$LASTEXITCODE"
