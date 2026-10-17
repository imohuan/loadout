$ok = Get-Content "$env:TEMP\log_ok.json" -Raw | ConvertFrom-Json
$bad = Get-Content "$env:TEMP\log_bad.json" -Raw | ConvertFrom-Json

Write-Output '=== FAILED request (all fields) ==='
Write-Output ("model=" + $bad.model + " channel=" + $bad.channel + " stream=" + $bad.stream + " status=" + $bad.http_status)
Write-Output ("headers:")
$bad.request_json.headers | ConvertTo-Json

Write-Output '=== OK request (meta) ==='
Write-Output ("model=" + $ok.model + " channel=" + $ok.channel + " stream=" + $ok.stream + " status=" + $ok.http_status + " result=" + $ok.result)
Write-Output 'headers:'
$ok.request_json.headers | ConvertTo-Json

Write-Output '=== OK body top-level keys ==='
$b = $ok.request_json.body | ConvertFrom-Json
$b.PSObject.Properties | ForEach-Object {
  $v = $_.Value
  if ($_.Name -eq 'messages') {
    Write-Output ("messages: count=" + $v.Count)
    for ($i=0; $i -lt $v.Count; $i++) {
      $m = $v[$i]
      $c = ''
      if ($m.content -is [string]) { $c = 'len=' + $m.content.Length }
      else { $c = ($m.content | ConvertTo-Json -Compress -Depth 4) }
      $line = "  [$i] role=" + $m.role + " " + $c
      if ($line.Length -gt 400) { $line = $line.Substring(0,400) + '...' }
      Write-Output $line
    }
  } elseif ($v -is [System.Array]) {
    Write-Output ($_.Name + ": array count=" + $v.Count + " first=" + ($v | Select-Object -First 1 | ConvertTo-Json -Compress -Depth 3))
  } else {
    $s = ($v | ConvertTo-Json -Compress -Depth 3)
    if ($s.Length -gt 300) { $s = $s.Substring(0,300) + '...' }
    Write-Output ($_.Name + " = " + $s)
  }
}
