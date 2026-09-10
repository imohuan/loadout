Write-Output '=== 所有 loadout 相关进程 ==='
Get-CimInstance Win32_Process -Filter "Name='loadout.exe' OR Name='loadout-verify.exe'" -ErrorAction SilentlyContinue |
  Select-Object ProcessId, Name, ExecutablePath, CreationDate | Format-Table -AutoSize

Write-Output '=== 3000 / 3111 监听 ==='
foreach ($port in 3000, 3111) {
  $c = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
  if ($c) { Write-Output "$port -> pid $($c[0].OwningProcess)" } else { Write-Output "$port -> free" }
}

Write-Output '=== bin/loadout.exe 构建时间 ==='
Get-Item 'D:\Code\Git\loadout\bin\loadout.exe' | Select-Object LastWriteTime, Length | Format-List
