# bench/compare.ps1 — A/B benchmark runner + comparison.
#
# Work-order §2.2 / §2.3: run the SAME bench suite against two git refs
# (baseline vs optimized), save raw output under bench/results/<ref>/,
# and print a comparison table. If benchstat is on PATH we defer to it;
# otherwise we compute mean / median / stdev in PowerShell (the documented
# offline fallback).
#
# Usage:
#   pwsh bench/compare.ps1 -RefA baseline-v1 -RefB perf/optimization
#   pwsh bench/compare.ps1 -RefA HEAD~1 -RefB HEAD -Count 3 -BenchTime 100x
#
# Requirements:
#   - Clean worktree (we `git checkout` between runs).
#   - No uncommitted changes you care about (we refuse to run if dirty).
#   - go.exe at $GoExe.

[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$RefA,
    [Parameter(Mandatory=$true)][string]$RefB,
    [int]$Count = 1,
    [string]$BenchTime = "100x",
    [string]$GoExe = "D:\1_2027_AI\opencode\tools\go\bin\go.exe",
    [string]$BenchPattern = ".",
    [switch]$SkipGitCheck
)

$ErrorActionPreference = "Stop"

# ── locate src/ ─────────────────────────────────────────────────────────
$benchDir = $PSScriptRoot
$src = Split-Path -Parent $benchDir
$resultsRoot = Join-Path $benchDir "results"

# ── sanity: clean worktree (unless skipped) ─────────────────────────────
if (-not $SkipGitCheck) {
    Push-Location $src
    try {
        $dirty = git status --porcelain
        if ($dirty) {
            throw "Worktree is dirty; commit or stash before running A/B (or pass -SkipGitCheck).`n$dirty"
        }
    } finally { Pop-Location }
}

# ── helpers ─────────────────────────────────────────────────────────────

function Get-RefSha([string]$ref) {
    Push-Location $src
    try {
        return (git rev-parse --short $ref).Trim()
    } finally { Pop-Location }
}

function Invoke-BenchSuite([string]$ref) {
    <#
      .SYNOPSIS
        Check out $ref, run L1+L2 benches, write raw output to
        bench/results/<sha>/ and return the file path.
    #>
    $sha = Get-RefSha $ref
    $outDir = Join-Path $resultsRoot "$ref-$sha"
    New-Item -ItemType Directory -Path $outDir -Force | Out-Null

    Push-Location $src
    try {
        Write-Host "==> checkout $ref ($sha)" -ForegroundColor Cyan
        git checkout --quiet $ref
        if ($LASTEXITCODE -ne 0) { throw "git checkout $ref failed" }

        $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
        $microFile = Join-Path $outDir "micro-$stamp.txt"
        $moduleFile = Join-Path $outDir "module-$stamp.txt"

        Write-Host "==> L1 micro -> $microFile" -ForegroundColor Cyan
        & $GoExe test -tags bench -bench $BenchPattern -benchmem -benchtime $BenchTime -count=$Count -run='^$' ./bench/micro/ `
            | Tee-Object -FilePath $microFile
        if ($LASTEXITCODE -ne 0) { throw "micro bench failed ($LASTEXITCODE)" }

        Write-Host "==> L2 module -> $moduleFile" -ForegroundColor Cyan
        & $GoExe test -tags bench -bench $BenchPattern -benchmem -benchtime $BenchTime -count=$Count -run='^$' ./bench/module/ `
            | Tee-Object -FilePath $moduleFile
        if ($LASTEXITCODE -ne 0) { throw "module bench failed ($LASTEXITCODE)" }

        return @($microFile, $moduleFile)
    } finally { Pop-Location }
}

# ── parse go bench output ───────────────────────────────────────────────
# Line shape: BenchmarkX-16  50  12345 ns/op  1234 B/op  7 allocs/op
$script:LineRx = [regex]'^(?<name>Benchmark\S+)-\d+\s+\d+\s+(?<ns>[\d\.]+)\s+ns/op(?:\s+(?<mbps>[\d\.]+)\s+MB/s)?\s+(?<bop>\d+)\s+B/op\s+(?<allocs>\d+)\s+allocs/op'

function Parse-BenchFile([string]$path) {
    $map = @{}
    foreach ($line in Get-Content $path) {
        $m = $script:LineRx.Match($line)
        if (-not $m.Success) { continue }
        $name = $m.Groups['name'].Value
        if (-not $map.ContainsKey($name)) {
            $map[$name] = @{ ns = @(); bop = @(); allocs = @() }
        }
        $map[$name].ns      += [double]$m.Groups['ns'].Value
        $map[$name].bop     += [double]$m.Groups['bop'].Value
        $map[$name].allocs  += [double]$m.Groups['allocs'].Value
    }
    return $map
}

function Get-Median([double[]]$xs) {
    if ($xs.Count -eq 0) { return 0 }
    $s = $xs | Sort-Object
    $mid = [int]($s.Count / 2)
    if ($s.Count % 2 -eq 1) { return $s[$mid] }
    return ($s[$mid - 1] + $s[$mid]) / 2
}

function Get-Stdev([double[]]$xs) {
    if ($xs.Count -lt 2) { return 0 }
    $mean = ($xs | Measure-Object -Average).Average
    $var = ($xs | ForEach-Object { ($_ - $mean) * ($_ - $mean) } | Measure-Object -Sum).Sum / ($xs.Count - 1)
    return [Math]::Sqrt($var)
}

function Summarize($map) {
    $out = @{}
    foreach ($k in $map.Keys) {
        $out[$k] = @{
            nsMedian     = Get-Median $map[$k].ns
            nsMean       = ($map[$k].ns | Measure-Object -Average).Average
            nsStdev      = Get-Stdev $map[$k].ns
            bopMedian    = Get-Median $map[$k].bop
            allocsMedian = Get-Median $map[$k].allocs
            samples      = $map[$k].ns.Count
        }
    }
    return $out
}

# ── benchstat fast path ─────────────────────────────────────────────────
$benchstat = Get-Command benchstat -ErrorAction SilentlyContinue

# ── run both refs ───────────────────────────────────────────────────────
$filesA = Invoke-BenchSuite $RefA
$filesB = Invoke-BenchSuite $RefB

# restore original ref
Push-Location $src
try {
    git checkout --quiet -
} finally { Pop-Location }

# ── comparison ──────────────────────────────────────────────────────────
if ($benchstat) {
    Write-Host "`n==> benchstat comparison" -ForegroundColor Cyan
    foreach ($pair in @(@($filesA[0], $filesB[0]), @($filesA[1], $filesB[1]))) {
        & benchstat $pair[0] $pair[1]
    }
    return
}

# PowerShell fallback: median + relative change
Write-Host "`n==> Comparison (PowerShell fallback; install benchstat for significance testing)" -ForegroundColor Cyan

$allRows = @()
foreach ($pair in @(@{label='L1 micro'; a=$filesA[0]; b=$filesB[0]}, @{label='L2 module'; a=$filesA[1]; b=$filesB[1]})) {
    $sa = Summarize (Parse-BenchFile $pair.a)
    $sb = Summarize (Parse-BenchFile $pair.b)
    $names = $sa.Keys + $sb.Keys | Sort-Object -Unique
    foreach ($n in $names) {
        $row = [ordered]@{
            Suite       = $pair.label
            Benchmark   = $n
            A_ns_median = if ($sa.ContainsKey($n)) { [Math]::Round($sa[$n].nsMedian, 2) } else { $null }
            B_ns_median = if ($sb.ContainsKey($n)) { [Math]::Round($sb[$n].nsMedian, 2) } else { $null }
            NsChange    = $null
            A_B_op      = if ($sa.ContainsKey($n)) { [int]$sa[$n].bopMedian } else { $null }
            B_B_op      = if ($sb.ContainsKey($n)) { [int]$sb[$n].bopMedian } else { $null }
            A_allocs    = if ($sa.ContainsKey($n)) { [int]$sa[$n].allocsMedian } else { $null }
            B_allocs    = if ($sb.ContainsKey($n)) { [int]$sb[$n].allocsMedian } else { $null }
        }
        if ($sa.ContainsKey($n) -and $sb.ContainsKey($n) -and $sa[$n].nsMedian -ne 0) {
            $pct = ($sb[$n].nsMedian - $sa[$n].nsMedian) / $sa[$n].nsMedian * 100
            $row.NsChange = ("{0:+0.0;-0.0;0}%" -f $pct)
        }
        $allRows += [pscustomobject]$row
    }
}

$allRows | Format-Table -AutoSize

# Persist a markdown copy next to the raw outputs
$reportPath = Join-Path $resultsRoot ("compare-{0}-vs-{1}-{2:yyyyMMdd-HHmmss}.md" -f $RefA, $RefB, (Get-Date))
$md = @()
$md += "# A/B bench comparison — $RefA vs $RefB"
$md += ""
$md += ("- generated: {0:u}" -f (Get-Date))
$md += ("- go: {0}" -f (& $GoExe version))
$md += ("- count: $Count, benchtime: $BenchTime")
$md += ""
$md += "| Suite | Benchmark | A ns/op (median) | B ns/op (median) | Δ ns | A B/op | B B/op | A allocs | B allocs |"
$md += "|---|---|---:|---:|---:|---:|---:|---:|---:|"
foreach ($r in $allRows) {
    $md += "| $($r.Suite) | $($r.Benchmark) | $($r.A_ns_median) | $($r.B_ns_median) | $($r.NsChange) | $($r.A_B_op) | $($r.B_B_op) | $($r.A_allocs) | $($r.B_allocs) |"
}
$md -join "`n" | Set-Content -LiteralPath $reportPath -Encoding UTF8
Write-Host "`nwrote $reportPath" -ForegroundColor Green
