param(
    [string]$InstallPath = (Join-Path (Split-Path -Parent $PSScriptRoot) "dist\opencode-host.exe")
)

# Transport selection:
#   To force the newly launched Host to use WSS, set this in the same
#   PowerShell session before running this script:
#       $env:OCHOST_DISABLE_QUIC = "1"
#   Start-Process inherits that process-scoped setting. It affects this Host
#   launch only; remove the variable before a later deployment to restore the
#   default QUIC-first behavior (with WSS fallback).

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$goEnvScript = Join-Path $PSScriptRoot "goenv.ps1"
$installPath = [System.IO.Path]::GetFullPath($InstallPath)
$installDir = Split-Path -Parent $installPath
$stagePath = Join-Path $installDir ".opencode-host-$PID.new.exe"
$backupPath = "$installPath.bak"
$logDir = Join-Path $env:TEMP "capri-opencode-host"
$stdoutLog = Join-Path $logDir "stdout.log"
$stderrLog = Join-Path $logDir "stderr.log"
$oldProcesses = @()
$newProcess = $null
$oldProcessesStopped = $false
$hadPreviousBinary = Test-Path -LiteralPath $installPath -PathType Leaf

if ([string]::IsNullOrWhiteSpace($env:OCHOST_HUB_URL)) {
    throw "OCHOST_HUB_URL is not set in this PowerShell session. Set it before updating the remote host."
}
if ([string]::IsNullOrWhiteSpace($env:OCHOST_HOST_TOKEN)) {
    throw "OCHOST_HOST_TOKEN is not set in this PowerShell session. Set the paired host token before updating."
}

if (Test-Path -LiteralPath $goEnvScript -PathType Leaf) {
    . $goEnvScript
}
else {
    throw "Go environment script not found: $goEnvScript"
}

New-Item -ItemType Directory -Path $installDir -Force | Out-Null

try {
    Push-Location $repoRoot
    try {
        Write-Host "Building opencode-host..."
        & go build -o $stagePath ./capri-host/cmd/opencode-host
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }

    $targetFullPath = [System.IO.Path]::GetFullPath($installPath)
    $oldProcesses = @(
        Get-CimInstance Win32_Process -Filter "Name = 'opencode-host.exe'" |
            Where-Object {
                $_.ExecutablePath -and
                [System.IO.Path]::GetFullPath($_.ExecutablePath).Equals(
                    $targetFullPath,
                    [System.StringComparison]::OrdinalIgnoreCase
                )
            }
    )

    if ($hadPreviousBinary) {
        Copy-Item -LiteralPath $installPath -Destination $backupPath -Force
    }

    foreach ($process in $oldProcesses) {
        Write-Host "Stopping old opencode-host process $($process.ProcessId) and its child processes..."
        & taskkill.exe /PID $process.ProcessId /T /F | Out-Null
        if ($LASTEXITCODE -ne 0 -and (Get-Process -Id $process.ProcessId -ErrorAction SilentlyContinue)) {
            throw "Could not stop opencode-host process $($process.ProcessId)."
        }
    }

    foreach ($process in $oldProcesses) {
        $deadline = (Get-Date).AddSeconds(8)
        while ((Get-Process -Id $process.ProcessId -ErrorAction SilentlyContinue) -and (Get-Date) -lt $deadline) {
            Start-Sleep -Milliseconds 200
        }
        if (Get-Process -Id $process.ProcessId -ErrorAction SilentlyContinue) {
            throw "Old opencode-host process $($process.ProcessId) did not exit."
        }
    }

    $oldProcessesStopped = $true
    Move-Item -LiteralPath $stagePath -Destination $installPath -Force

    Write-Host "Starting updated opencode-host..."
    New-Item -ItemType Directory -Path $logDir -Force | Out-Null
    $newProcess = Start-Process -FilePath $installPath -WorkingDirectory $repoRoot -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog

    # opencode-host's own OpenCode health startup deadline is 45 seconds;
    # allow it to report a real startup error before the deployment rolls back.
    $deadline = (Get-Date).AddSeconds(60)
    $gatewayReady = $false
    while ((Get-Date) -lt $deadline) {
        $newProcess.Refresh()
        if ($newProcess.HasExited) {
            $errorText = if (Test-Path -LiteralPath $stderrLog) { Get-Content -LiteralPath $stderrLog -Raw } else { "" }
            $outputText = if (Test-Path -LiteralPath $stdoutLog) { Get-Content -LiteralPath $stdoutLog -Raw } else { "" }
            throw "Updated opencode-host exited during startup (code $($newProcess.ExitCode)). Output: $outputText $errorText"
        }
        if (Get-NetTCPConnection -LocalPort 41800 -State Listen -ErrorAction SilentlyContinue) {
            $gatewayReady = $true
            break
        }
        Start-Sleep -Milliseconds 500
    }
    if (-not $gatewayReady) {
        throw "Updated opencode-host stayed running but did not open local gateway port 41800 within 60 seconds. Logs: $stdoutLog and $stderrLog"
    }

    Write-Host "Updated opencode-host is running and local gateway is listening (PID $($newProcess.Id))."
    Write-Host "Host logs: $stdoutLog and $stderrLog"
    Write-Host "Previous executable backup: $backupPath"
}
catch {
    if ($newProcess -and -not $newProcess.HasExited) {
        & taskkill.exe /PID $newProcess.Id /T /F | Out-Null
    }

    if ($oldProcessesStopped -and $hadPreviousBinary -and (Test-Path -LiteralPath $backupPath -PathType Leaf)) {
        Copy-Item -LiteralPath $backupPath -Destination $installPath -Force
        Write-Warning "Restored the previous opencode-host executable."
        $restored = Start-Process -FilePath $installPath -WorkingDirectory $repoRoot -PassThru
        Write-Host "Previous opencode-host restarted (PID $($restored.Id))."
    }
    elseif ($hadPreviousBinary -and (Test-Path -LiteralPath $backupPath -PathType Leaf)) {
        Copy-Item -LiteralPath $backupPath -Destination $installPath -Force
        Write-Warning "Restored the previous opencode-host executable."
    }

    throw
}
finally {
    if (Test-Path -LiteralPath $stagePath -PathType Leaf) {
        Remove-Item -LiteralPath $stagePath -Force
    }
}
