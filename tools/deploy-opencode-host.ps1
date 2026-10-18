param(
    [string]$InstallPath = (Join-Path (Split-Path -Parent $PSScriptRoot) "dist\opencode-host.exe")
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$goEnvScript = Join-Path $PSScriptRoot "goenv.ps1"
$installPath = [System.IO.Path]::GetFullPath($InstallPath)
$installDir = Split-Path -Parent $installPath
$stagePath = Join-Path $installDir ".opencode-host-$PID.new.exe"
$backupPath = "$installPath.bak"
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
    $newProcess = Start-Process -FilePath $installPath -WorkingDirectory $repoRoot -PassThru
    Start-Sleep -Seconds 2
    $newProcess.Refresh()
    if ($newProcess.HasExited) {
        throw "Updated opencode-host exited during startup (code $($newProcess.ExitCode))."
    }

    Write-Host "Updated opencode-host is running (PID $($newProcess.Id))."
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
