[CmdletBinding()]
param(
  [ValidateSet('Start', 'Stop', 'Status')]
  [string]$Action,
  [Parameter(Mandatory)]
  [string]$RunId,
  [string]$BinaryPath,
  [string]$SourceDatabase = 'one-api.db',
  [int]$UpperPort = 3000,
  [int]$LowerPort = 3001,
  [switch]$Apply
)

$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path (Join-Path $scriptDir '..\..')).Path
$runDir = Join-Path $repoRoot ".local-tests\opencodego-affinity-rpm\$RunId"
$processFile = Join-Path $runDir 'dual-instance-processes.json'

function Assert-InRunDirectory {
  param([string]$Path)
  $absolute = [IO.Path]::GetFullPath($Path)
  $prefix = [IO.Path]::GetFullPath($runDir) + [IO.Path]::DirectorySeparatorChar
  if (-not $absolute.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Target is outside the run directory: $absolute"
  }
  return $absolute
}

function Get-Health {
  param([int]$Port)
  try {
    $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 -Uri "http://127.0.0.1:$Port/api/status"
    return $response.StatusCode -eq 200
  } catch {
    return $false
  }
}

function Wait-Health {
  param([int]$Port)
  $deadline = (Get-Date).AddSeconds(60)
  while ((Get-Date) -lt $deadline) {
    if (Get-Health $Port) { return }
    Start-Sleep -Milliseconds 500
  }
  throw "Instance on port $Port did not become healthy within 60 seconds."
}

if ($Action -eq 'Status') {
  if (-not (Test-Path -LiteralPath $processFile)) { throw "Process manifest not found: $processFile" }
  $manifest = Get-Content -LiteralPath $processFile -Raw | ConvertFrom-Json
  [pscustomobject]@{
    UpperPid = $manifest.upper.pid
    UpperRunning = [bool](Get-Process -Id $manifest.upper.pid -ErrorAction SilentlyContinue)
    UpperHealthy = Get-Health $UpperPort
    LowerPid = $manifest.lower.pid
    LowerRunning = [bool](Get-Process -Id $manifest.lower.pid -ErrorAction SilentlyContinue)
    LowerHealthy = Get-Health $LowerPort
  }
  exit 0
}

if (-not $Apply) { throw "Action $Action changes test processes/files; re-run with -Apply." }

if ($Action -eq 'Stop') {
  if (-not (Test-Path -LiteralPath $processFile)) { throw "Process manifest not found: $processFile" }
  $manifest = Get-Content -LiteralPath $processFile -Raw | ConvertFrom-Json
  foreach ($entry in @($manifest.upper, $manifest.lower)) {
    $process = Get-Process -Id $entry.pid -ErrorAction SilentlyContinue
    if (-not $process) { continue }
    $actualPath = $process.Path
    if (-not $actualPath -or [IO.Path]::GetFullPath($actualPath) -ne [IO.Path]::GetFullPath($entry.binary)) {
      throw "PID $($entry.pid) no longer points to the recorded binary; refusing to stop it."
    }
    Stop-Process -Id $entry.pid
    Wait-Process -Id $entry.pid -Timeout 20 -ErrorAction SilentlyContinue
  }
  $stoppedManifest = Join-Path $runDir ("dual-instance-processes.stopped-{0}.json" -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
  Move-Item -LiteralPath $processFile -Destination $stoppedManifest
  exit 0
}

foreach ($name in @('AFFINITY_SECRET', 'SESSION_SECRET', 'CRYPTO_SECRET', 'OCG_TEST_REDIS_URL')) {
  if (-not (Get-Item "Env:$name" -ErrorAction SilentlyContinue).Value) {
    throw "Environment variable $name is required."
  }
}
if (-not $BinaryPath) {
  $BinaryPath = Join-Path $runDir 'new-api-test.exe'
}
$binaryAbsolute = (Resolve-Path -LiteralPath $BinaryPath).Path
$sourceAbsolute = (Resolve-Path -LiteralPath $SourceDatabase).Path
$upperDatabase = Assert-InRunDirectory (Join-Path $runDir 'upper.db')
$lowerDatabase = Assert-InRunDirectory (Join-Path $runDir 'lower.db')
if (Test-Path -LiteralPath $processFile) {
  throw "A dual-instance process manifest already exists. Use -Action Status or Stop before starting again."
}
if ((Get-NetTCPConnection -State Listen -LocalPort $UpperPort,$LowerPort -ErrorAction SilentlyContinue).Count -gt 0) {
  throw "Port $UpperPort or $LowerPort is already listening. Stop the exact existing test instance before continuing."
}
if ((Test-Path -LiteralPath $upperDatabase) -or (Test-Path -LiteralPath $lowerDatabase)) {
  throw 'upper.db or lower.db already exists. Preserve it for diagnostics, then move it aside or start a new RunId.'
}
Copy-Item -LiteralPath $sourceAbsolute -Destination $upperDatabase
Copy-Item -LiteralPath $sourceAbsolute -Destination $lowerDatabase

$saved = @{}
foreach ($name in @('PORT', 'NODE_NAME', 'SQLITE_PATH', 'REDIS_CONN_STRING')) {
  $saved[$name] = (Get-Item "Env:$name" -ErrorAction SilentlyContinue).Value
}
try {
  $env:REDIS_CONN_STRING = $env:OCG_TEST_REDIS_URL
  $env:PORT = "$UpperPort"
  $env:NODE_NAME = 'ocg-upper-test'
  $env:SQLITE_PATH = $upperDatabase
  $upper = Start-Process -FilePath $binaryAbsolute -WorkingDirectory $runDir -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput (Join-Path $runDir 'upper.stdout.log') `
    -RedirectStandardError (Join-Path $runDir 'upper.stderr.log')

  $env:PORT = "$LowerPort"
  $env:NODE_NAME = 'ocg-lower-test'
  $env:SQLITE_PATH = $lowerDatabase
  $lower = Start-Process -FilePath $binaryAbsolute -WorkingDirectory $runDir -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput (Join-Path $runDir 'lower.stdout.log') `
    -RedirectStandardError (Join-Path $runDir 'lower.stderr.log')
} finally {
  foreach ($name in $saved.Keys) {
    if ($null -eq $saved[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
    else { Set-Item "Env:$name" $saved[$name] }
  }
}

try {
  Wait-Health $UpperPort
  Wait-Health $LowerPort
} catch {
  foreach ($process in @($upper, $lower)) {
    if ($process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force }
  }
  throw
}

$manifest = [ordered]@{
  started_at = (Get-Date).ToUniversalTime().ToString('o')
  binary_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryAbsolute).Hash.ToLowerInvariant()
  upper = [ordered]@{ pid = $upper.Id; port = $UpperPort; node_name = 'ocg-upper-test'; database = $upperDatabase; binary = $binaryAbsolute }
  lower = [ordered]@{ pid = $lower.Id; port = $LowerPort; node_name = 'ocg-lower-test'; database = $lowerDatabase; binary = $binaryAbsolute }
}
$manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $processFile -Encoding utf8NoBOM
$manifest | ConvertTo-Json -Depth 5
