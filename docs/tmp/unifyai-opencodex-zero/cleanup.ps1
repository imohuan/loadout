Write-Output '--- stopping my verify instances ---'
$mine = Get-CimInstance Win32_Process -Filter "Name='loadout-verify.exe'" -ErrorAction SilentlyContinue
if ($mine) {
  foreach ($p in $mine) {
    Write-Output "stopping pid=$($p.ProcessId)"
    Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
  }
} else { Write-Output 'none' }

Start-Sleep -Seconds 2

Write-Output ''
Write-Output '--- port 3111 (should be free) ---'
$c = Get-NetTCPConnection -State Listen -LocalPort 3111 -ErrorAction SilentlyContinue
if ($c) { Write-Output "still busy: pid $($c[0].OwningProcess)" } else { Write-Output 'free' }

Write-Output ''
Write-Output '--- user service (must stay alive) ---'
Get-CimInstance Win32_Process -Filter "Name='loadout.exe'" -ErrorAction SilentlyContinue |
  Select-Object ProcessId, ExecutablePath, CreationDate | Format-Table -AutoSize

Write-Output '--- port 3000 ---'
$c3 = Get-NetTCPConnection -State Listen -LocalPort 3000 -ErrorAction SilentlyContinue
if ($c3) { Write-Output "3000 -> pid $($c3[0].OwningProcess)" } else { Write-Output '3000 free (user service stopped)' }
