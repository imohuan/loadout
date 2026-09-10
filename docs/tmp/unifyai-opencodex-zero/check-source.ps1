$syncPath = 'C:\Users\Administrator\.unifyai\sync.json'
Write-Output "exists: $(Test-Path $syncPath)"
$cfg = Get-Content $syncPath -Raw | ConvertFrom-Json
Write-Output '=== sync.json 全部字段 ==='
$cfg | ConvertTo-Json -Depth 5

$src = $cfg.source
Write-Output ''
Write-Output "raw source: [$src]"

$expanded = $src
if ($src -eq '~') { $expanded = 'C:\Users\Administrator' }
elseif ($src.StartsWith('~/') -or $src.StartsWith('~\')) {
  $expanded = Join-Path 'C:\Users\Administrator' $src.Substring(2)
}
Write-Output "expandHome: [$expanded]"
Write-Output "file exists: $(Test-Path $expanded)"
