$p = 'D:\Code\Git\loadout\frontend\src\views\RouteLogsView.vue'
$lines = Get-Content $p
for ($i = 0; $i -lt $lines.Count; $i++) {
  if ($lines[$i] -match 'refreshAllStats') { Write-Output "$($i+1): $($lines[$i])" }
}
