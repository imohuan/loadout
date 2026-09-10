Set-Location 'D:\Code\Git\unifyai'
Write-Output '--- syntax check ---'
node --check src/core/config-loader.mjs
Write-Output "syntax-exit=$LASTEXITCODE"
if ($LASTEXITCODE -ne 0) { exit 1 }

git add src/core/config-loader.mjs
git commit -F 'D:\Code\Git\loadout\docs\tmp\unifyai-opencodex-zero\commit-msg.txt'
Write-Output '--- log ---'
git log --oneline -3
