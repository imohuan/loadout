# Check whether port 3000 is in use (default Loadout server port).
# Exit code 0 = free, 1 = in use (blocks the VSCode debug preLaunchTask).
# Keep this file pure ASCII - Windows PowerShell 5.1 decodes files as
# system ANSI (GBK) when there is no BOM, and multi-byte comments can
# corrupt parsing.

$ErrorActionPreference = 'Stop'

$conns = Get-NetTCPConnection -LocalPort 3000 -State Listen -ErrorAction SilentlyContinue
if ($conns) {
    $pids = ($conns | Select-Object -ExpandProperty OwningProcess -Unique) -join ', '
    Write-Host "Port 3000 is in use, PIDs: $pids"
    Write-Host 'Close it first (run task: kill-port-3000) or change LOADOUT_SERVER_ADDR.'
    exit 1
}

# Fallback check via netstat (always available).
# Filter LISTENING lines first, then require an exact port boundary so
# that e.g. :30001 is not misreported as :3000.
$netstatHit = netstat -ano | Select-String 'LISTENING' | Select-String ':3000\s'
if ($netstatHit) {
    Write-Host 'Port 3000 is in use (netstat check).'
    Write-Host 'Close it first (run task: kill-port-3000) or change LOADOUT_SERVER_ADDR.'
    exit 1
}

Write-Host 'Port 3000 is free, ready to debug.'
exit 0
