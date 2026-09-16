param([int]$RootPid, [string]$Out, [int]$IntervalMs = 500)
# Sums working set and private bytes over a process and all its descendants
# (forge-worker, the venv launcher python.exe, and the real python it starts).
"time,procs,ws_total,priv_total,ws_python,priv_python,ws_root,cpu_load" | Out-File -FilePath $Out -Encoding ascii
while ($true) {
  $all = Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId,Name,WorkingSetSize,PrivatePageCount
  $root = $all | Where-Object { $_.ProcessId -eq $RootPid }
  if (-not $root) { break }
  $set = @{}; $set[$RootPid] = $true
  $grew = $true
  while ($grew) {
    $grew = $false
    foreach ($p in $all) {
      if (-not $set.ContainsKey([int]$p.ProcessId) -and $set.ContainsKey([int]$p.ParentProcessId)) {
        $set[[int]$p.ProcessId] = $true; $grew = $true
      }
    }
  }
  $ws = 0; $priv = 0; $pyws = 0; $pypriv = 0; $n = 0
  foreach ($p in $all) {
    if ($set.ContainsKey([int]$p.ProcessId)) {
      $n++; $ws += [int64]$p.WorkingSetSize; $priv += [int64]$p.PrivatePageCount
      if ($p.Name -like 'python*') { $pyws += [int64]$p.WorkingSetSize; $pypriv += [int64]$p.PrivatePageCount }
    }
  }
  $load = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average
  "$(Get-Date -Format o),$n,$ws,$priv,$pyws,$pypriv,$([int64]$root.WorkingSetSize),$load" | Out-File -FilePath $Out -Append -Encoding ascii
  Start-Sleep -Milliseconds $IntervalMs
}
