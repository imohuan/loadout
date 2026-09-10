$procId = 24272
$p = Get-CimInstance Win32_Process -Filter "ProcessId=$procId" -ErrorAction SilentlyContinue
if ($null -eq $p) { Write-Output "pid $procId not found"; exit 0 }
$p | Select-Object ProcessId, Name, ExecutablePath, CommandLine | Format-List

Write-Output '--- bin/loadout.exe mtime (my rebuild) ---'
Get-Item 'D:\Code\Git\loadout\bin\loadout.exe' | Select-Object LastWriteTime, Length | Format-List

Write-Output '--- unifyai installed timeout (npm global) ---'
Select-String -Path 'C:\Users\Administrator\AppData\Roaming\npm\node_modules\unifyai\src\core\config-loader.mjs' -Pattern 'controller.abort\(\)' | Select-Object -First 3
