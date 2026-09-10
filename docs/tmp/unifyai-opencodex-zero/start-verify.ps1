$ErrorActionPreference = 'Continue'
$exe = "$env:TEMP\loadout-verify.exe"
$port = 3111
$out = "$env:TEMP\loadout-verify.log"
$err = "$env:TEMP\loadout-verify.err"
"starting $exe on port $port"
$p = Start-Process -FilePath $exe -ArgumentList "--port", "$port" -PassThru -RedirectStandardOutput $out -RedirectStandardError $err -WindowStyle Hidden
"pid=$($p.Id)"
Start-Sleep -Seconds 6
"--- stdout ---"
Get-Content $out -ErrorAction SilentlyContinue | Select-Object -First 20
"--- stderr ---"
Get-Content $err -ErrorAction SilentlyContinue | Select-Object -First 20
"pid alive: $(-not $p.HasExited)"
$p.Id | Out-File "$env:TEMP\loadout-verify.pid"
