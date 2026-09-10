Get-Process | Where-Object { $_.ProcessName -like 'loadout*' -or $_.ProcessName -eq 'go' } |
  Select-Object Id, ProcessName, StartTime, Path |
  Format-Table -AutoSize | Out-String -Width 200
