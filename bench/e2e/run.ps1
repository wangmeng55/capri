# bench/e2e/run.ps1 — L3 harness driver.
#
# T0.2 scope: invoke scenarios.go against a target and stash the JSON
# under bench/results/<ref>/. Later phases will add hub+host bring-up
# (S1 against a real tunnel), fan-out (S5), large-body POST (S3), WS
# forward (S4), SSE (S2), and idle CPU/RSS (S6).
#
# Usage:
#   pwsh bench/e2e/run.ps1 -Ref baseline-v1 -Scenario S1 -Target http://127.0.0.1:8787/global/health -N 200

[CmdletBinding()]
param(
    [string]$Ref = "local",
    [string]$Scenario = "S1",
    [string]$Target = "http://127.0.0.1:8787/global/health",
    [int]$N = 200,
    [int]$Warmup = 20,
    [string]$GoExe = "D:\1_2027_AI\opencode\tools\go\bin\go.exe"
)

$ErrorActionPreference = "Stop"

$src = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)  # bench/e2e -> bench -> src
$resultsDir = Join-Path $src "bench\results\$Ref"
New-Item -ItemType Directory -Path $resultsDir -Force | Out-Null

$outFile = Join-Path $resultsDir ("e2e-{0}-{1:yyyyMMdd-HHmmss}.json" -f $Scenario, (Get-Date))

Push-Location $src
try {
    & $GoExe run -tags bench ./bench/e2e `
        -scenario $Scenario `
        -target $Target `
        -n $N `
        -warmup $Warmup `
        -out $outFile
    if ($LASTEXITCODE -ne 0) { throw "scenarios.go exited $LASTEXITCODE" }
    Write-Host "wrote $outFile"
} finally {
    Pop-Location
}
