$ErrorActionPreference = 'Continue'
Set-Location 'D:\Code\Git\loadout'
go build ./... 2>&1 | Tee-Object -Variable out
Write-Output "BUILD_EXIT=$LASTEXITCODE"
go vet ./plugins/route-log/... ./plugins/contracts/... ./plugins/admin-api/... 2>&1 | Tee-Object -Variable vout
Write-Output "VET_EXIT=$LASTEXITCODE"
