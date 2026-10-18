param(
    [string]$Vps = "azureuser@52.253.97.90",
    [string]$SshKey = (Join-Path $env:USERPROFILE "Downloads\xidian-linux_key.pem"),
    [string]$RemoteBinary = "/home/azureuser/capri-hub-linux-amd64"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$goEnvScript = Join-Path $PSScriptRoot "goenv.ps1"
$outputDir = Join-Path $repoRoot "dist"
$binary = Join-Path $outputDir "capri-hub-linux-amd64"
$remoteStage = "$RemoteBinary.new"
$gitVersion = "unknown"
if (Get-Command git -ErrorAction SilentlyContinue) {
    $gitOutput = & git -C $repoRoot rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $gitOutput) {
        $gitVersion = ("$gitOutput").Trim()
    }
}
$buildVersion = "$gitVersion-$(Get-Date -Format 'yyyyMMdd-HHmmss')"

if (-not (Test-Path -LiteralPath $SshKey -PathType Leaf)) {
    throw "SSH key not found: $SshKey"
}

if (Test-Path -LiteralPath $goEnvScript -PathType Leaf) {
    . $goEnvScript
}
else {
    $portableGoRoot = Join-Path (Split-Path -Parent $repoRoot) "tools\go"
    if (Test-Path (Join-Path $portableGoRoot "bin\go.exe")) {
        $env:GOROOT = $portableGoRoot
        $env:PATH = "$(Join-Path $portableGoRoot 'bin');$env:PATH"
    }
    elseif (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "Go was not found. Install Go or place the portable toolchain at ..\tools\go."
    }

    if (-not $env:GOPATH) { $env:GOPATH = Join-Path $repoRoot ".gopath" }
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $env:GOPATH "pkg\mod" }
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $repoRoot ".gocache" }
}
New-Item -ItemType Directory -Path $outputDir -Force | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED
try {
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    Push-Location $repoRoot
    try {
        Write-Host "Building Capri hub for Linux amd64..."
        & go build -ldflags "-X main.version=$buildVersion" -o $binary ./capri-hub/cmd/capri-hub
        if ($LASTEXITCODE -ne 0) { throw "Go build failed with exit code $LASTEXITCODE" }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgo
}

$sshArgs = @("-i", $SshKey, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new")
Write-Host "Uploading binary to $Vps..."
& scp @sshArgs $binary "${Vps}:$remoteStage"
if ($LASTEXITCODE -ne 0) { throw "Upload failed with exit code $LASTEXITCODE" }

$remoteScript = @"
set -eu
sudo systemctl stop capri-hub
cp -p '$RemoteBinary' '${RemoteBinary}.bak'
mv '$remoteStage' '$RemoteBinary'
chmod 755 '$RemoteBinary'
if sudo systemctl start capri-hub && sudo systemctl is-active --quiet capri-hub; then
  echo 'Capri hub deployment succeeded.'
  sudo systemctl is-active capri-hub
  sudo journalctl -u capri-hub -n 6 --no-pager
else
  echo 'New binary failed to start; restoring previous binary.' >&2
  sudo systemctl stop capri-hub || true
  mv '${RemoteBinary}.bak' '$RemoteBinary'
  sudo systemctl start capri-hub
  exit 1
fi
"@
$remoteEncoded = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($remoteScript))
$remoteCommand = "printf '%s' '$remoteEncoded' | base64 -d | bash"

Write-Host "Installing and restarting Capri hub..."
& ssh @sshArgs $Vps $remoteCommand
if ($LASTEXITCODE -ne 0) { throw "Remote install failed with exit code $LASTEXITCODE" }

Write-Host "Deployment complete. Hub version: $buildVersion"
Write-Host "Refresh the Capri page if you changed the hub UI."
