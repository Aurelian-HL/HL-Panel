param(
    [Parameter(Mandatory = $true)]
    [string] $ReleaseDirectory
)

$ErrorActionPreference = 'Stop'

$release = (Resolve-Path -LiteralPath $ReleaseDirectory).Path
$manifestPath = Join-Path $release 'SHA256SUMS'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "SHA256SUMS is missing from $release"
}

$required = @(
    'bin/control-api',
    'bin/usage-migrate',
    'bin/edge-agent',
    'web-admin/index.html',
    'deploy/README.md',
    'deploy/install.sh',
    'deploy/enable-domain-tls.sh',
    'deploy/edge-agent/enroll.sh',
    'deploy/edge-agent/install.sh',
    'deploy/env/control-api.env.example',
    'deploy/nginx/hl-panel.conf.template',
    'deploy/nginx/conf.d/hl-panel-rate-limit.conf',
    'deploy/nginx/snippets/hl-panel-api-proxy.conf',
    'deploy/nginx/snippets/hl-panel-app-locations.conf',
    'deploy/nginx/snippets/hl-panel-security-headers.conf',
    'deploy/systemd/hl-panel-control-api.service',
    'deploy/systemd/hl-panel-edge-agent.service'
)
foreach ($relativePath in $required) {
    $path = Join-Path $release ($relativePath -replace '/', [IO.Path]::DirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "required release binary is missing: $relativePath"
    }
}

foreach ($forbidden in @('deploy/deploy', 'deploy/deploy-current')) {
    $forbiddenPath = Join-Path $release ($forbidden -replace '/', [IO.Path]::DirectorySeparatorChar)
    if (Test-Path -LiteralPath $forbiddenPath) {
        throw "release contains a duplicated deployment directory: $forbidden"
    }
}

function Normalize-ManifestPath([string] $value) {
    $normalized = $value.Trim() -replace '\\', '/'
    if ([string]::IsNullOrWhiteSpace($normalized) -or $normalized.StartsWith('/') -or $normalized -match '(^|/)\.\.?(/|$)') {
        throw "invalid path in SHA256SUMS: $value"
    }
    return $normalized
}

$entries = @{}
foreach ($line in Get-Content -LiteralPath $manifestPath) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    if ($line -notmatch '^\s*([0-9A-Fa-f]{64})\s+\*?(.+?)\s*$') {
        throw "invalid SHA256SUMS line: $line"
    }
    $digest = $Matches[1].ToUpperInvariant()
    $relativePath = Normalize-ManifestPath $Matches[2]
    if ($entries.ContainsKey($relativePath)) {
        throw "duplicate SHA256SUMS entry: $relativePath"
    }
    $entries[$relativePath] = $digest
}

foreach ($entry in $entries.GetEnumerator()) {
    $path = Join-Path $release ($entry.Key -replace '/', [IO.Path]::DirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "SHA256SUMS references a missing file: $($entry.Key)"
    }
    $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToUpperInvariant()
    if ($actual -ne $entry.Value) {
        throw "SHA256 mismatch for $($entry.Key)"
    }
}

$actualFiles = @{}
Get-ChildItem -LiteralPath $release -Recurse -File | Where-Object { $_.FullName -ne $manifestPath } | ForEach-Object {
    $relative = $_.FullName.Substring($release.Length + 1) -replace '\\', '/'
    $actualFiles[$relative] = $true
}
foreach ($relativePath in $actualFiles.Keys) {
    if (-not $entries.ContainsKey($relativePath)) {
        throw "release file is not covered by SHA256SUMS: $relativePath"
    }
    if ($relativePath.StartsWith('deploy/')) {
        $deploymentPath = $relativePath.Substring('deploy/'.Length)
        $allowedDeploymentPath = $required | Where-Object { $_.StartsWith('deploy/') } | ForEach-Object { $_.Substring('deploy/'.Length) }
        if ($deploymentPath -notin $allowedDeploymentPath) {
            throw "release contains an unexpected deployment asset: $relativePath"
        }
    }
}
foreach ($relativePath in $entries.Keys) {
    if (-not $actualFiles.ContainsKey($relativePath)) {
        throw "SHA256SUMS contains a file outside the release: $relativePath"
    }
}

Write-Output "release verified: $release"
Write-Output "files=$($actualFiles.Count) required_binaries=$($required.Count)"
