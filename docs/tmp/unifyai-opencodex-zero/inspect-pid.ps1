$procId = 32772
$p = Get-CimInstance Win32_Process -Filter "ProcessId=$procId" -ErrorAction SilentlyContinue
if ($null -eq $p) { Write-Output "pid $procId not found"; exit 0 }
$p | Select-Object ProcessId, Name, ExecutablePath, CommandLine | Format-List
