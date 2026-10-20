param(
    [Parameter(Mandatory = $true)]
    [string] $ReleaseDirectory,

    [string] $GoExecutable = 'go',

    [string] $WebDistDirectory = 'apps/web-admin/dist',

    [string] $Version = 'development',

    [string] $PythonExecutable = 'python',

    [string] $ZipPath,

    [string] $GostBundleDirectory,

    [string] $GostManifestSha256,

    [string] $LicenseIssuerURL,
    [string] $LicenseIssuerID,
    [string] $LicensePublicKeys
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($LicenseIssuerURL) -or [string]::IsNullOrWhiteSpace($LicenseIssuerID) -or [string]::IsNullOrWhiteSpace($LicensePublicKeys)) { throw 'Commercial release requires LicenseIssuerURL, LicenseIssuerID and LicensePublicKeys (key ID to raw base64url public key JSON)' }
if ($LicenseIssuerURL -notmatch '^https://[A-Za-z0-9.-]+(:[0-9]+)?/?$' -or $LicenseIssuerID -notmatch '^https://[A-Za-z0-9./:_-]+$') { throw 'Invalid pinned issuer identity or URL' }
$parsedLicenseKeys = $LicensePublicKeys | ConvertFrom-Json
if ($parsedLicenseKeys -isnot [PSCustomObject] -or @($parsedLicenseKeys.PSObject.Properties).Count -eq 0) { throw 'At least one pinned issuer public key is required' }
foreach ($entry in $parsedLicenseKeys.PSObject.Properties) { if ($entry.Name -notmatch '^[A-Za-z0-9_-]+$' -or $entry.Value -notmatch '^[A-Za-z0-9_-]{43}$') { throw 'Invalid Ed25519 issuer public key' } }
$licenseKeysB64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(($parsedLicenseKeys | ConvertTo-Json -Compress))).TrimEnd('=').Replace('+', '-').Replace('/', '_')
$hasGostBundle = -not [string]::IsNullOrWhiteSpace($GostBundleDirectory)
$hasGostManifest = -not [string]::IsNullOrWhiteSpace($GostManifestSha256)
if ($hasGostBundle -ne $hasGostManifest) {
    throw 'GOST bundle directory and reviewed manifest SHA256 must be supplied together'
}
$engineArguments = @()
if ($hasGostBundle) {
    if ($GostManifestSha256 -cnotmatch '^[0-9a-f]{64}$') {
        throw 'reviewed GOST manifest SHA256 must contain exactly 64 lowercase hexadecimal characters'
    }
    if (-not (Test-Path -LiteralPath $GostBundleDirectory -PathType Container)) {
        throw "GOST bundle directory does not exist: $GostBundleDirectory"
    }
    $gostBundlePath = (Resolve-Path -LiteralPath $GostBundleDirectory).Path
    $engineArguments = @('--gost-bundle', $gostBundlePath, '--gost-manifest-sha256', $GostManifestSha256)
}

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
# Share the official CI trust validator, including canonical Ed25519 decoding
# and rejection of the disposable test-only signing identity.
$trustEnvironment = @{
    HL_PANEL_LICENSE_ISSUER_URL = $LicenseIssuerURL
    HL_PANEL_LICENSE_ISSUER_ID = $LicenseIssuerID
    HL_PANEL_LICENSE_PUBLIC_KEYS_B64 = $licenseKeysB64
}
$previousTrustEnvironment = @{}
try {
    foreach ($name in $trustEnvironment.Keys) {
        $previousTrustEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $trustEnvironment[$name], 'Process')
    }
    $licenseFlags = & $PythonExecutable (Join-Path $PSScriptRoot 'build-license-trust.py')
    if ($LASTEXITCODE -ne 0) { throw 'Official license trust validation failed' }
} finally {
    foreach ($name in $previousTrustEnvironment.Keys) {
        if ($null -eq $previousTrustEnvironment[$name]) {
            Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($name, $previousTrustEnvironment[$name], 'Process')
        }
    }
}
if ([IO.Path]::IsPathRooted($ReleaseDirectory)) {
    $releasePath = [IO.Path]::GetFullPath($ReleaseDirectory)
} else {
    $releasePath = [IO.Path]::GetFullPath((Join-Path (Get-Location) $ReleaseDirectory))
}
$releaseParent = Split-Path -Parent $releasePath
$releaseName = Split-Path -Leaf $releasePath
$stagePath = Join-Path $releaseParent ('.{0}.staging-{1}' -f $releaseName, $PID)

if (Test-Path -LiteralPath $releasePath) {
    throw "release directory already exists; choose a new path: $releasePath"
}
if (Test-Path -LiteralPath $stagePath) {
    throw "staging directory already exists: $stagePath"
}

if ([IO.Path]::IsPathRooted($WebDistDirectory)) {
    $webDistPath = [IO.Path]::GetFullPath($WebDistDirectory)
} else {
    $webDistPath = [IO.Path]::GetFullPath((Join-Path (Get-Location) $WebDistDirectory))
}
if (-not (Test-Path -LiteralPath $webDistPath -PathType Container)) {
    throw "web dist directory does not exist: $webDistPath"
}

$goCommand = Get-Command $GoExecutable -ErrorAction SilentlyContinue
if ($null -eq $goCommand) {
    throw "Go executable was not found: $GoExecutable"
}

$oldGoOS = $env:GOOS
$oldGoArch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED
$oldGoToolchain = $env:GOTOOLCHAIN

function Restore-Environment {
    if ($null -eq $oldGoOS) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGoOS }
    if ($null -eq $oldGoArch) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGoArch }
    if ($null -eq $oldCgo) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $oldCgo }
    if ($null -eq $oldGoToolchain) { Remove-Item Env:GOTOOLCHAIN -ErrorAction SilentlyContinue } else { $env:GOTOOLCHAIN = $oldGoToolchain }
}

try {
    New-Item -ItemType Directory -Path (Join-Path $stagePath 'bin') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $stagePath 'web-admin') -Force | Out-Null

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $env:GOTOOLCHAIN = 'local'

    $targets = @(
        @{ Name = 'control-api'; Package = './apps/control-api' },
        @{ Name = 'usage-migrate'; Package = './apps/usage-migrate' },
        @{ Name = 'edge-agent'; Package = './apps/edge-agent' }
    )
    foreach ($target in $targets) {
        $outputPath = Join-Path $stagePath ('bin/{0}' -f $target.Name)
        $buildFlags = @('-trimpath')
        if ($target.Name -eq 'control-api') { $buildFlags += @('-ldflags', "-X main.platformVersion=$Version $licenseFlags") }
        if ($target.Name -eq 'edge-agent') { $buildFlags += @('-ldflags', "-X github.com/hongle/hl-panel/internal/agent/config.DefaultAgentVersion=$Version") }
        & $goCommand.Source build @buildFlags -o $outputPath $target.Package
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed for $($target.Name) with exit code $LASTEXITCODE"
        }
    }
    & $PythonExecutable (Join-Path $PSScriptRoot 'fetch-node-engines.py') $stagePath @engineArguments
    if ($LASTEXITCODE -ne 0) { throw 'pinned node engine download or verification failed' }

    $deploymentFiles = @(
        'README.md',
        'panel-license.md',
        'backup-and-restore.md',
        'install.sh',
        'activate-panel-license.sh',
        'setup-panel-license.sh',
        'check-panel-license.py',
        'install-node.sh',
        'reinstall-node.py',
        'configure-node-bbr2.py',
        'configure-node-bbr.py',
        'fetch-node-engines.py',
        'update.sh',
        'update.py',
        'update-node.py',
        'panel-update-worker.py',
        'node-update-worker.py',
        'install-node-update.py',
        'configure-reality.sh',
        'configure-reality.py',
        'enable-domain-tls.sh',
        'edge-agent\enroll.sh',
        'edge-agent\install.sh',
        'env\control-api.env.example',
        'nginx\hl-panel.conf.template',
        'nginx\conf.d\hl-panel-rate-limit.conf',
        'nginx\snippets\hl-panel-api-proxy.conf',
        'nginx\snippets\hl-panel-migration-proxy.conf',
        'node-control-url.py',
        'nginx\snippets\hl-panel-app-locations.conf',
        'nginx\snippets\hl-panel-security-headers.conf',
        'systemd\hl-panel-control-api.service',
        'systemd\hl-panel-update.socket',
        'systemd\hl-panel-update.service',
        'systemd\hl-panel-node-update.socket',
        'systemd\hl-panel-node-update.service',
        'systemd\hl-panel-edge-agent.service'
    )
    foreach ($relativePath in $deploymentFiles) {
        $sourcePath = Join-Path $repoRoot (Join-Path 'deploy' $relativePath)
        if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
            throw "required deployment asset is missing: deploy/$relativePath"
        }
        $destinationPath = Join-Path $stagePath (Join-Path 'deploy' $relativePath)
        $destinationDirectory = Split-Path -Parent $destinationPath
        New-Item -ItemType Directory -Path $destinationDirectory -Force | Out-Null
        Copy-Item -LiteralPath $sourcePath -Destination $destinationPath
    }
    Copy-Item -Path (Join-Path $webDistPath '*') -Destination (Join-Path $stagePath 'web-admin') -Recurse -Force

    $manifestPath = Join-Path $stagePath 'SHA256SUMS'
    $manifestLines = Get-ChildItem -LiteralPath $stagePath -Recurse -File |
        Where-Object { $_.FullName -ne $manifestPath } |
        Sort-Object FullName |
        ForEach-Object {
            $relative = $_.FullName.Substring($stagePath.Length + 1) -replace '\\', '/'
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            '{0}  {1}' -f $hash, $relative
        }
    Set-Content -LiteralPath $manifestPath -Value $manifestLines -Encoding ascii

    & (Join-Path $PSScriptRoot 'verify-release.ps1') -ReleaseDirectory $stagePath
    if ($LASTEXITCODE -ne 0) {
        throw "release verification failed with exit code $LASTEXITCODE"
    }

    Move-Item -LiteralPath $stagePath -Destination $releasePath

    if (-not [string]::IsNullOrWhiteSpace($ZipPath)) {
        if ([IO.Path]::IsPathRooted($ZipPath)) {
            $zipFile = [IO.Path]::GetFullPath($ZipPath)
        } else {
            $zipFile = [IO.Path]::GetFullPath((Join-Path (Get-Location) $ZipPath))
        }
        if (Test-Path -LiteralPath $zipFile) {
            throw "zip path already exists: $zipFile"
        }
        Compress-Archive -LiteralPath $releasePath -DestinationPath $zipFile -CompressionLevel Optimal
        Write-Output "zip=$zipFile"
    }
    Write-Output "release=$releasePath"
}
finally {
    Restore-Environment
    if (Test-Path -LiteralPath $stagePath) {
        Remove-Item -LiteralPath $stagePath -Recurse -Force
    }
}
