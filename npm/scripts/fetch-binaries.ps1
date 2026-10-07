$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$vendor = Join-Path $root 'vendor'
$version = (Get-Content (Join-Path $root 'package.json') | ConvertFrom-Json).version
$base = "https://github.com/mevarx/GoCode/releases/download/v$version"

$assets = @{
  'linux-amd64'   = "gocode_${version}_linux_amd64.tar.gz"
  'linux-arm64'   = "gocode_${version}_linux_arm64.tar.gz"
  'darwin-amd64'  = "gocode_${version}_darwin_amd64.tar.gz"
  'darwin-arm64'  = "gocode_${version}_darwin_arm64.tar.gz"
  'windows-amd64' = "gocode_${version}_windows_amd64.zip"
  'windows-arm64' = "gocode_${version}_windows_arm64.zip"
}

foreach ($dir in $assets.Keys) {
  $dest = Join-Path $vendor $dir
  New-Item -ItemType Directory -Force $dest | Out-Null
  $url = "$base/$( $assets[$dir] )"
  $tmp = Join-Path $env:TEMP $assets[$dir]
  Invoke-WebRequest $url -OutFile $tmp
  $binName = if ($dir.StartsWith('windows')) { 'gocode.exe' } else { 'gocode' }
  $binPath = Join-Path $dest $binName
  if ($assets[$dir].EndsWith('.zip')) {
    Expand-Archive $tmp -DestinationPath $dest -Force
  } else {
    tar -xzf $tmp -C $dest $binName 2>$null
    if (-not (Test-Path $binPath)) { tar -xzf $tmp -C $dest }
  }
  Remove-Item $tmp -Force
}
Write-Host "binaries ready in $vendor"
