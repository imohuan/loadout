# Kill all processes listening on port 3000.
# Manual task (kill-port-3000), not part of the debug preLaunchTask.
# Pure ASCII only - see note in check-port-3000.ps1 about PS 5.1 encoding.

$ErrorActionPreference = 'SilentlyContinue'

$conns = Get-NetTCPConnection -LocalPort 3000 -State Listen -ErrorAction SilentlyContinue
if (-not $conns) {
    Write-Host 'No process is using port 3000.'
    exit 0
}

$procIds = $conns | Select-Object -ExpandProperty OwningProcess -Unique
foreach ($procId in $procIds) {
    Write-Host "Killing PID $procId"
    Stop-Process -Id $procId -Force
}

$survivors = Get-NetTCPConnection -LocalPort 3000 -State Listen -ErrorAction SilentlyContinue
if ($survivors) {
    Write-Host 'Some processes survived (need elevated permissions?).'
    exit 1
}

Write-Host 'Port 3000 is now free.'
exit 0
