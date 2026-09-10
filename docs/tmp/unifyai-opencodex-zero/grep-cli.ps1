$f = 'C:\Users\Administrator\AppData\Roaming\npm\node_modules\unifyai\src\cli.mjs'
Select-String -Path $f -Pattern "listModels|listAll|'all'|models:" |
  Select-Object LineNumber, Line |
  Format-Table -AutoSize -Wrap | Out-String -Width 190
