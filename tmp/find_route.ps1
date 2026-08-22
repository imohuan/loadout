Get-ChildItem -Recurse -Filter *.go apps/server | Select-String -Pattern 'route-logs' | ForEach-Object { $_.Path + ':' + $_.LineNumber }  
