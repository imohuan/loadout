$root = Join-Path $env:APPDATA 'npm\node_modules\unifyai'
Get-ChildItem $root -Recurse -File -Include *.mjs, *.js |
  Where-Object { $_.FullName -notmatch 'node_modules\\' } |
  Select-Object @{n='Rel';e={$_.FullName.Substring($root.Length+1)}}, Length |
  Format-Table -AutoSize | Out-String -Width 160
