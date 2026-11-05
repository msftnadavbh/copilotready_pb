# Records every TCP connection held by Copilot processes started from the e2e folder.
param([string]$Prefix, [string]$Out)
while (-not (Test-Path "$Out.stop")) {
  foreach ($p in (Get-Process -Name copilot -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$Prefix*" })) {
    foreach ($c in (Get-NetTCPConnection -OwningProcess $p.Id -ErrorAction SilentlyContinue)) {
      "$((Get-Date).ToUniversalTime().ToString('o')) pid=$($p.Id) local=$($c.LocalAddress):$($c.LocalPort) remote=$($c.RemoteAddress):$($c.RemotePort) $($c.State)" | Add-Content -Path $Out
    }
  }
  Start-Sleep -Milliseconds 150
}
