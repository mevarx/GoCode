param([string]$Version)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$vendor = Join-Path $root 'vendor'
if (-not $Version) {
  $Version = (& gh release view --json tagName -q .tagName 2>$null).TrimStart('v')
  if (-not $Version) {
    $Version = (& gh api repos/mevarx/GoCode/releases/latest -q .tag_name).TrimStart('v')
  }
}
if (-not $Version) { throw "could not resolve release version; pass -Version" }
# keep package.json in lockstep with the fetched binaries
$pkgPath = Join-Path $root 'package.json'
$pkg = Get-Content $pkgPath | ConvertFrom-Json
$pkg.version = $Version
$pkg | ConvertTo-Json -Depth 10 | Set-Content $pkgPath
$version = $Version
$base = "https://github.com/mevarx/GoCode/releases/download/v$version"

$assets = @{
  'linux-amd64'   = "gocode_${version}_linux_amd64.tar.gz"
  'linux-arm64'   = "gocode_${version}_linux_arm64.tar.gz"
  'darwin-amd64'  = "gocode_${version}_darwin_amd64.tar.gz"
  'darwin-arm64'  = "gocode_${version}_darwin_arm64.tar.gz"
  'windows-amd64' = "gocode_${version}_windows_amd64.zip"
  'windows-arm64' = "gocode_${version}_windows_arm64.zip"
}

# tar comes from PATH (present on Linux/macOS runners); never hardcode a Windows path.
$tarCmd = Get-Command tar -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $tarCmd) { throw "tar executable not found on PATH" }
$tarPath = $tarCmd.Source

# Per-run temp directory under the platform temp root (TMPDIR on Linux, %TEMP% on Windows).
$tmpRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("gocode-fetch-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

try {
  # GoReleaser checksums.txt: "<sha256>  <filename>" per line.
  $checksumsPath = Join-Path $tmpRoot 'checksums.txt'
  Invoke-WebRequest "$base/checksums.txt" -OutFile $checksumsPath
  $checksums = @{}
  foreach ($line in Get-Content -Path $checksumsPath) {
    $line = $line.Trim()
    if (-not $line) { continue }
    $parts = $line -split '\s+'
    if ($parts.Count -lt 2) { continue }
    $hash = $parts[0]
    $name = $parts[-1].TrimStart('*')
    if ($hash -match '^[0-9a-fA-F]{64}$' -and $name) {
      $checksums[$name] = $hash.ToLowerInvariant()
    }
  }
  if ($checksums.Count -eq 0) { throw "no checksums parsed from $base/checksums.txt" }

  foreach ($dir in $assets.Keys) {
    $asset = $assets[$dir]
    $dest = Join-Path $vendor $dir
    New-Item -ItemType Directory -Force $dest | Out-Null
    $url = "$base/$asset"
    $tmp = Join-Path $tmpRoot $asset
    Invoke-WebRequest $url -OutFile $tmp

    if (-not $checksums.ContainsKey($asset)) {
      throw "no checksum entry for $asset in checksums.txt; refusing to extract"
    }
    $expected = $checksums[$asset]
    $actual = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
      throw "checksum mismatch for $asset`: expected $expected, got $actual; refusing to extract"
    }

    $binName = if ($dir.StartsWith('windows')) { 'gocode.exe' } else { 'gocode' }
    $binPath = Join-Path $dest $binName
    if ($asset.EndsWith('.zip')) {
        Expand-Archive $tmp -DestinationPath $dest -Force
    } else {
        & $tarPath -xzf $tmp -C $dest $binName 2>$null
        if (-not (Test-Path $binPath)) { & $tarPath -xzf $tmp -C $dest }
    }
  }
  Write-Host "binaries ready in $vendor"
} finally {
  if (Test-Path -LiteralPath $tmpRoot) {
    Remove-Item -LiteralPath $tmpRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}
