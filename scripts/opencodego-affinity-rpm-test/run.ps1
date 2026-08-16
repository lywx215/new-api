[CmdletBinding()]
param(
  [string]$RunId = (Get-Date -Format 'yyyyMMdd-HHmmss'),
  [switch]$Resume,
  [string]$Step,
  [string]$DatabasePath = 'one-api.db',
  [string]$OuterDatabasePath = '',
  [string]$BinaryPath = '',
  [string]$BaseUrl = 'http://127.0.0.1:3000',
  [string]$LowerBaseUrl = 'http://127.0.0.1:3001',
  [string]$RedisUrl = $env:OCG_TEST_REDIS_URL,
  [switch]$Apply,
  [switch]$SkipCodeValidation
)

$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path (Join-Path $scriptDir '..\..')).Path
$runRoot = Join-Path $repoRoot '.local-tests\opencodego-affinity-rpm'
$runDir = Join-Path $runRoot $RunId
$toolDir = Join-Path $runRoot '_tool'
$toolPath = Join-Path $toolDir 'ocg-affinity-rpm-test.exe'

function Find-Go {
  $command = Get-Command go.exe -ErrorAction SilentlyContinue
  if ($command) { return $command.Source }
  $candidates = Get-ChildItem -Path (Join-Path $env:USERPROFILE '.cache\codex-runtimes') -Filter go.exe -Recurse -ErrorAction SilentlyContinue |
    Where-Object FullName -Match '\\go\\bin\\go\.exe$' |
    Where-Object { Test-Path -LiteralPath (Join-Path (Split-Path -Parent (Split-Path -Parent $_.FullName)) 'src\runtime') } |
    Sort-Object @{ Expression = { $_.FullName -match '-complete' }; Descending = $true }, FullName -Descending
  if ($candidates.Count -gt 0) { return $candidates[0].FullName }
  throw 'Go executable was not found. Install Go 1.22+ or add go.exe to PATH.'
}

function Invoke-Tool {
  param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
  & $toolPath @Arguments
  return $LASTEXITCODE
}

function Get-InputHash {
  param([string]$Name)
  $baselinePath = Join-Path $runDir 'input-baseline.txt'
  if (-not (Test-Path -LiteralPath $baselinePath)) { throw 'Run input baseline is missing.' }
  $material = "$Name|$(Get-Content -LiteralPath $baselinePath -Raw)"
  $bytes = [Text.Encoding]::UTF8.GetBytes($material)
  return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-SensitiveInputHash {
  param([string]$Value)
  if (-not $Value) { return 'none' }
  $bytes = [Text.Encoding]::UTF8.GetBytes($Value)
  return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-SourceIdentity {
  $material = [Collections.Generic.List[string]]::new()
  $material.Add((& git -C $repoRoot rev-parse HEAD 2>$null))
  $material.AddRange([string[]]@(& git -C $repoRoot diff --binary --no-ext-diff -- .))
  $untracked = @(& git -C $repoRoot ls-files --others --exclude-standard | Where-Object { $_ -notlike 'tmp-fork-scan/*' } | Sort-Object)
  foreach ($relativePath in $untracked) {
    $fullPath = Join-Path $repoRoot $relativePath
    if (Test-Path -LiteralPath $fullPath -PathType Leaf) {
      $material.Add("$relativePath=" + (Get-FileHash -Algorithm SHA256 -LiteralPath $fullPath).Hash)
    }
  }
  return Get-SensitiveInputHash ($material -join "`n")
}

function Set-StepState {
  param([string]$Name, [string]$Status, [string]$Hash = '', [string]$Message = '', [string[]]$Artifacts = @())
  $args = @('state', '--run-dir', $runDir, '--step', $Name, '--status', $Status)
  if ($Hash) { $args += @('--input-hash', $Hash) }
  if ($Message) { $args += @('--error', $Message) }
  if ($Artifacts.Count -gt 0) { $args += @('--artifacts', ($Artifacts -join ',')) }
  & $toolPath @args
  return $LASTEXITCODE
}

function Invoke-LoggedCommand {
  param([string]$Name, [string]$WorkingDirectory, [string]$Executable, [string[]]$Arguments)
  $logPath = Join-Path $runDir ("code-validation-{0}.log" -f $Name)
  Push-Location $WorkingDirectory
  try {
    & $Executable @Arguments 2>&1 | Tee-Object -FilePath $logPath
    if ($LASTEXITCODE -ne 0) { throw "$Name failed with exit code $LASTEXITCODE" }
  } finally {
    Pop-Location
  }
}

function Invoke-CodeValidation {
  if ($SkipCodeValidation) { throw 'Code validation cannot be skipped in a complete acceptance run.' }
  $go = Find-Go
  $failures = [Collections.Generic.List[string]]::new()
  try { Invoke-LoggedCommand 'root-go-test' $repoRoot $go @('test', '-p', '1', './...') } catch { $failures.Add($_.Exception.Message) }
  $oldWork = $env:GOWORK
  try {
    $env:GOWORK = 'off'
    try { Invoke-LoggedCommand 'relaykit-go-test' (Join-Path $repoRoot 'relaykit') $go @('test', './...') } catch { $failures.Add($_.Exception.Message) }
    try { Invoke-LoggedCommand 'relaykit-go-build' (Join-Path $repoRoot 'relaykit') $go @('build', './...') } catch { $failures.Add($_.Exception.Message) }
  } finally {
    $env:GOWORK = $oldWork
  }
  $bun = Get-Command bun.exe -ErrorAction SilentlyContinue
  if (-not $bun) { throw 'bun.exe is not on PATH; install Bun before frontend validation.' }
  try { Invoke-LoggedCommand 'web-i18n-sync' (Join-Path $repoRoot 'web') $bun.Source @('run', 'i18n:sync') } catch { $failures.Add($_.Exception.Message) }
  try { Invoke-LoggedCommand 'web-typecheck' (Join-Path $repoRoot 'web') $bun.Source @('run', 'typecheck') } catch { $failures.Add($_.Exception.Message) }

  $changedFrontend = @(& git -C $repoRoot diff --name-only --diff-filter=ACMR -- 'web/*.ts' 'web/*.tsx') +
    @(& git -C $repoRoot ls-files --others --exclude-standard -- 'web/*.ts' 'web/*.tsx')
  $changedFrontend = @($changedFrontend | Where-Object { $_ } | Sort-Object -Unique | ForEach-Object { $_ -replace '^web/', '' })
  if ($changedFrontend.Count -gt 0) {
    try { Invoke-LoggedCommand 'web-changed-files-lint' (Join-Path $repoRoot 'web') $bun.Source (@('x', 'oxlint', '-c', '.oxlintrc.json') + $changedFrontend) } catch { $failures.Add($_.Exception.Message) }
  }
  try { Invoke-LoggedCommand 'web-lint' (Join-Path $repoRoot 'web') $bun.Source @('run', 'lint') } catch { Write-Warning ("Full repository lint baseline remains non-zero: " + $_.Exception.Message) }
  try { Invoke-LoggedCommand 'web-build' (Join-Path $repoRoot 'web') $bun.Source @('run', 'build') } catch { $failures.Add($_.Exception.Message) }
  try { Invoke-LoggedCommand 'git-diff-check' $repoRoot 'git.exe' @('diff', '--check') } catch { $failures.Add($_.Exception.Message) }
  if ($failures.Count -gt 0) {
    throw ('Code validation failures: ' + ($failures -join '; '))
  }
}

function Invoke-TestStep {
  param([string]$Name)
  $inputHash = Get-InputHash $Name
  $stateCode = Set-StepState $Name 'running' $inputHash
  if ($stateCode -ne 0) {
    if ($stateCode -eq 10) { return $true }
    throw "Could not start step $Name"
  }
  $stepLogName = "step-$Name.log"
  $transcriptStarted = $false
  try {
    Start-Transcript -LiteralPath (Join-Path $runDir $stepLogName) -Append | Out-Null
    $transcriptStarted = $true
  } catch {
    $transcriptStarted = $false
  }
  try {
    switch ($Name) {
      'inventory' {
        $args = @('inventory', '--run-dir', $runDir, '--run-id', $RunId, '--db', $DatabasePath, '--base-url', $BaseUrl)
        if ($BinaryPath) { $args += @('--binary', $BinaryPath) }
        & $toolPath @args
      }
      'code-validation' { Invoke-CodeValidation }
      'mock-selftest' { & $toolPath mock-selftest --run-dir $runDir }
      'redis-validation' {
        if (-not $RedisUrl) { throw [System.OperationCanceledException]::new('OCG_TEST_REDIS_URL is required after Memurai installation.') }
        & $toolPath redis-probe --run-dir $runDir --redis-url $RedisUrl
      }
      'upgrade' {
        & $toolPath backup --run-dir $runDir --db $DatabasePath --binary $BinaryPath
        $go = Find-Go
        $staged = Join-Path $runDir 'new-api-test.exe'
        Invoke-LoggedCommand 'new-api-build' $repoRoot $go @('build', '-o', $staged, '.')
        if (-not $Apply) { throw [System.OperationCanceledException]::new("Upgrade staged at $staged. Export the recorded runtime secrets, manually stop/start the exact test PID as described in the runbook, verify health, then mark this step passed.") }
        throw [System.OperationCanceledException]::new('Automatic replacement of an already-running instance remains intentionally gated even with -Apply. Stop/start the exact test PID with the recorded environment, verify health, then mark this step passed.')
      }
      'verify-channels' { & $toolPath verify-channels --run-dir $runDir --db $DatabasePath }
      'api-snapshot' { & $toolPath api-snapshot --run-dir $runDir --base-url $BaseUrl }
      'provision-loopback' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply to merge groups and provision tokens/channel.') }
        & $toolPath provision-groups --run-dir $runDir --run-id $RunId --base-url $BaseUrl --apply
        if ($LASTEXITCODE -eq 0) { & $toolPath provision-tokens --run-dir $runDir --base-url $BaseUrl --apply }
        if ($LASTEXITCODE -eq 0) { & $toolPath provision-loopback --run-dir $runDir --base-url $BaseUrl --upstream-base-url $BaseUrl --apply }
      }
      'mock-gateway-e2e' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply; this step provisions isolated Mock channels and sends deterministic gateway requests.') }
        & $toolPath apply-profile --run-dir $runDir --run-id $RunId --base-url $BaseUrl --profile single-low --apply
        if ($LASTEXITCODE -eq 0) { & $toolPath mock-gateway-e2e --run-dir $runDir --run-id $RunId --base-url $BaseUrl --db $DatabasePath --redis-url $RedisUrl --apply }
      }
      'affinity-smoke' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply; this step changes the affinity profile and sends real requests.') }
        & $toolPath apply-profile --run-dir $runDir --run-id $RunId --base-url $BaseUrl --profile single-affinity --apply
        if ($LASTEXITCODE -eq 0) { & $toolPath affinity-smoke --run-dir $runDir --run-id $RunId --base-url $BaseUrl --db $DatabasePath }
      }
      'cache-migration-low-rpm' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply; this step changes RPM settings and sends real requests.') }
        & $toolPath apply-profile --run-dir $runDir --run-id $RunId --base-url $BaseUrl --profile single-low --apply
        if ($LASTEXITCODE -eq 0) {
          $cacheArgs = @('cache-migration', '--run-dir', $runDir, '--run-id', $RunId, '--base-url', $BaseUrl, '--db', $DatabasePath, '--redis-url', $RedisUrl)
          if ($OuterDatabasePath) { $cacheArgs += @('--outer-db', $OuterDatabasePath) }
          & $toolPath @cacheArgs
        }
      }
      'redis-failopen' { throw [System.OperationCanceledException]::new('Start this deterministic fault scenario only after the three Mock channels are configured; see runbook Step 10.') }
      'dual-instance' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Stop the exact single-instance PID, then re-run with -Apply to create and start the isolated upper/lower instances.') }
        $dualBinary = Join-Path $runDir 'new-api-test.exe'
        if (-not (Test-Path -LiteralPath $dualBinary)) { $dualBinary = $BinaryPath }
        & (Join-Path $scriptDir 'dual-instance.ps1') -Action Start -RunId $RunId -BinaryPath $dualBinary -SourceDatabase $DatabasePath -Apply
        if ($LASTEXITCODE -eq 0) { & $toolPath apply-profile --run-dir $runDir --run-id $RunId --base-url $BaseUrl --profile upper --apply }
        if ($LASTEXITCODE -eq 0) { & $toolPath apply-profile --run-dir $runDir --run-id $RunId --base-url $LowerBaseUrl --profile lower --apply }
        if ($LASTEXITCODE -eq 0) { & $toolPath provision-loopback --run-dir $runDir --base-url $BaseUrl --upstream-base-url $LowerBaseUrl --apply }
        if ($LASTEXITCODE -eq 0) { & $toolPath dual-smoke --run-dir $runDir --run-id $RunId --base-url $BaseUrl --lower-base-url $LowerBaseUrl --lower-db (Join-Path $runDir 'lower.db') }
      }
      'live-gate' { & $toolPath live-gate --run-dir $runDir --run-id $RunId }
      'hard-limit' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply and OCG_TEST_LIVE_CONFIRM after the live gate.') }
        & $toolPath hard-limit --run-dir $runDir --run-id $RunId --outer-base-url $BaseUrl --lower-base-url $LowerBaseUrl --db $DatabasePath --redis-url $RedisUrl --confirm $env:OCG_TEST_LIVE_CONFIRM
        if ($LASTEXITCODE -eq 0) { Set-StepState 'post429-cache-migration' 'passed' $inputHash '' @('hard-limit-post429-summary.json') | Out-Null }
      }
      'post429-cache-migration' {
        if (Test-Path (Join-Path $runDir 'hard-limit-post429-summary.json')) { return }
        throw [System.OperationCanceledException]::new('This scenario is executed atomically by the hard-limit step so the real cooldown is preserved.')
      }
      'three-customer-4800-rpm' {
        if (-not $Apply) { throw [System.OperationCanceledException]::new('Re-run with -Apply and OCG_TEST_LIVE_CONFIRM after reviewing the remaining budget.') }
        & $toolPath three-customer --run-dir $runDir --run-id $RunId --base-url $BaseUrl --db $DatabasePath --redis-url $RedisUrl --confirm $env:OCG_TEST_LIVE_CONFIRM
      }
      'resume-tests' { & $toolPath mock-selftest --run-dir $runDir }
      'report' { & $toolPath report --run-dir $runDir }
      'cleanup' { throw [System.OperationCanceledException]::new('Cleanup is confirmation-gated because real OpenCodeGo channels must be retained. Follow the exact-target checklist in the runbook.') }
      default { throw "Unknown step $Name" }
    }
    if ($LASTEXITCODE -ne 0) {
      if ($LASTEXITCODE -eq 20) { throw [System.OperationCanceledException]::new("Step $Name needs manual action.") }
      throw "Step $Name failed with exit code $LASTEXITCODE. Full stdout/stderr: $stepLogName"
    }
    Set-StepState $Name 'passed' $inputHash '' @($stepLogName) | Out-Null
    if ($Name -eq 'report') { & $toolPath report --run-dir $runDir }
  } catch [System.OperationCanceledException] {
    Set-StepState $Name 'needs_manual' $inputHash $_.Exception.Message @($stepLogName) | Out-Null
    & $toolPath report --run-dir $runDir
    Write-Warning $_.Exception.Message
    return $false
  } catch {
    Set-StepState $Name 'failed' $inputHash $_.Exception.Message @($stepLogName) | Out-Null
    & $toolPath report --run-dir $runDir
    throw
  } finally {
    if ($transcriptStarted) { Stop-Transcript | Out-Null }
  }
  return $true
}

Set-Location $repoRoot
New-Item -ItemType Directory -Path $toolDir -Force | Out-Null
$go = Find-Go
& $go build -o $toolPath ./scripts/opencodego-affinity-rpm-test
if ($LASTEXITCODE -ne 0) { throw 'Failed to build the OpenCodeGo test tool.' }
& $toolPath init --run-dir $runDir --run-id $RunId
if ($LASTEXITCODE -ne 0) { throw 'Failed to initialize or recover the run.' }
$baselinePath = Join-Path $runDir 'input-baseline.txt'
if (-not (Test-Path -LiteralPath $baselinePath)) {
  $sourceIdentity = Get-SourceIdentity
  $toolHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $toolPath).Hash
  $dbIdentity = 'missing'
  if (Test-Path -LiteralPath $DatabasePath) {
    $dbItem = Get-Item -LiteralPath $DatabasePath
    $dbIdentity = "$($dbItem.FullName),$($dbItem.Length),$($dbItem.LastWriteTimeUtc.Ticks)"
  }
  $redisIdentity = Get-SensitiveInputHash $RedisUrl
  "$sourceIdentity|$toolHash|$dbIdentity|$BinaryPath|$BaseUrl|$LowerBaseUrl|$OuterDatabasePath|$redisIdentity" | Set-Content -LiteralPath $baselinePath -Encoding utf8NoBOM
} else {
  $parts = (Get-Content -LiteralPath $baselinePath -Raw).Trim() -split '\|', 8
  $currentSourceIdentity = Get-SourceIdentity
  $currentToolHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $toolPath).Hash
  $redisIdentity = Get-SensitiveInputHash $RedisUrl
  $databaseAbsolute = if (Test-Path -LiteralPath $DatabasePath) { (Get-Item -LiteralPath $DatabasePath).FullName } else { $DatabasePath }
  $outerDatabaseAbsolute = if ($OuterDatabasePath -and (Test-Path -LiteralPath $OuterDatabasePath)) { (Get-Item -LiteralPath $OuterDatabasePath).FullName } else { $OuterDatabasePath }
  $inputsChanged = $parts.Count -lt 8 -or -not $parts[2].StartsWith("$databaseAbsolute,") -or
    $parts[3] -ne $BinaryPath -or $parts[4] -ne $BaseUrl -or $parts[5] -ne $LowerBaseUrl -or $parts[6] -ne $outerDatabaseAbsolute -or $parts[7] -ne $redisIdentity
  if ($parts.Count -lt 2 -or $parts[0] -ne $currentSourceIdentity -or $parts[1] -ne $currentToolHash -or $inputsChanged) {
    throw 'Code, test-tool hash, database path, binary path, service URL, or Redis URL changed after this run started. Use a new RunId; existing results remain available for audit.'
  }
}

if ($Step) {
  Invoke-TestStep $Step | Out-Null
  exit 0
}

$steps = @(
  'inventory', 'code-validation', 'mock-selftest', 'redis-validation', 'upgrade',
  'verify-channels', 'api-snapshot', 'provision-loopback', 'mock-gateway-e2e',
  'affinity-smoke', 'cache-migration-low-rpm', 'redis-failopen', 'dual-instance', 'live-gate',
  'hard-limit', 'post429-cache-migration', 'three-customer-4800-rpm',
  'resume-tests', 'report', 'cleanup'
)
foreach ($name in $steps) {
  $continued = Invoke-TestStep $name
  if (-not $continued) { break }
}

Write-Host "Run artifacts: $runDir"
