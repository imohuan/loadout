Set-Location 'D:\Code\Git\loadout'

Write-Output '=== service.go 最近提交 ==='
git log --oneline -4 -- plugins/unifyai/service.go

Write-Output ''
Write-Output '=== HEAD~1 里的 sourceFromSync ==='
$old = git show HEAD~1:plugins/unifyai/service.go
$idx = ($old | Select-String -Pattern 'func \(s \*Service\) sourceFromSync' | Select-Object -First 1).LineNumber
if ($idx) {
  $old[($idx-1)..($idx+14)]
} else {
  Write-Output 'not found'
}
