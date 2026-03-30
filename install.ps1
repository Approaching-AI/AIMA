#Requires -Version 5.1

$ErrorActionPreference = "Stop"

$AimaRepo = if ($env:AIMA_REPO) { $env:AIMA_REPO } else { "Approaching-AI/AIMA" }
$AimaVersion = $env:AIMA_VERSION
$AimaInstallDir = if ($env:AIMA_INSTALL_DIR) { $env:AIMA_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\AIMA" }

function Write-Info {
    param([string]$Message)
    Write-Host $Message
}

function Fail {
    param([string]$Message)
    throw $Message
}

function Get-GitHubHeaders {
    if ($env:GITHUB_TOKEN) {
        return @{ Authorization = "Bearer $($env:GITHUB_TOKEN)" }
    }
    return @{}
}

function Get-Json {
    param([string]$Url)
    Invoke-RestMethod -Headers (Get-GitHubHeaders) -Uri $Url
}

function Download-File {
    param(
        [string]$Url,
        [string]$OutFile
    )
    Invoke-WebRequest -Headers (Get-GitHubHeaders) -Uri $Url -OutFile $OutFile
}

function Get-PlatformAsset {
    if (-not [Environment]::Is64BitOperatingSystem) {
        Fail "Unsupported architecture: 32-bit Windows is not supported."
    }

    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { return "aima-windows-amd64.exe" }
        default { Fail "Unsupported Windows architecture: $($env:PROCESSOR_ARCHITECTURE). Expected AMD64." }
    }
}

function Get-LatestProductTag {
    $tags = Get-Json "https://api.github.com/repos/$AimaRepo/tags?per_page=100"
    $productTags = $tags |
        ForEach-Object { $_.name } |
        Where-Object { $_ -match '^v\d+\.\d+\.\d+$' } |
        Sort-Object { [version]($_.TrimStart('v')) }

    if (-not $productTags) {
        Fail "No product tag found in $AimaRepo."
    }

    return $productTags[-1]
}

function Get-LatestInstallableRelease {
    $releases = Get-Json "https://api.github.com/repos/$AimaRepo/releases?per_page=100"
    $productReleases = $releases |
        ForEach-Object { $_.tag_name } |
        Where-Object { $_ -match '^v\d+\.\d+\.\d+$' } |
        Sort-Object { [version]($_.TrimStart('v')) }

    if (-not $productReleases) {
        Fail "No installable product release found in $AimaRepo."
    }

    return $productReleases[-1]
}

function Get-ChecksumMap {
    param([string]$Path)

    $checksums = @{}
    foreach ($line in Get-Content -Path $Path) {
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }

        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2) {
            $checksums[$parts[1].Trim()] = $parts[0].Trim().ToLowerInvariant()
        }
    }

    return $checksums
}

function Add-ToUserPath {
    param([string]$Dir)

    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @()
    if ($current) {
        $parts = $current -split ';' | Where-Object { $_ }
    }

    if ($parts -contains $Dir) {
        return
    }

    $newPath = if ($current) { "$current;$Dir" } else { $Dir }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")

    $sessionParts = @()
    if ($env:Path) {
        $sessionParts = $env:Path -split ';' | Where-Object { $_ }
    }
    if (-not ($sessionParts -contains $Dir)) {
        $env:Path = if ($env:Path) { "$env:Path;$Dir" } else { $Dir }
    }

    Write-Info "Added $Dir to the current session and user PATH."
}

$asset = Get-PlatformAsset
$latestTag = Get-LatestProductTag
$version = if ($AimaVersion) { $AimaVersion } else { Get-LatestInstallableRelease }

Write-Info "AIMA repo: $AimaRepo"
Write-Info "AIMA version: $version"
Write-Info "AIMA asset: $asset"

if (-not $AimaVersion -and $version -ne $latestTag) {
    Write-Info "Warning: latest product tag is $latestTag, but latest installable binary release is $version."
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("aima-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
    $assetUrl = "https://github.com/$AimaRepo/releases/download/$version/$asset"
    $checksumsUrl = "https://github.com/$AimaRepo/releases/download/$version/checksums.txt"
    $assetPath = Join-Path $tmpDir $asset
    $checksumsPath = Join-Path $tmpDir "checksums.txt"

    try {
        Download-File -Url $assetUrl -OutFile $assetPath
    } catch {
        Fail "Release asset not found: $assetUrl. Publish $asset for tag $version first, or set AIMA_VERSION/AIMA_REPO."
    }

    try {
        Download-File -Url $checksumsUrl -OutFile $checksumsPath
    } catch {
        Fail "checksums.txt not found for $version. Publish release checksums before using the installer."
    }

    $checksums = Get-ChecksumMap -Path $checksumsPath
    $expected = $checksums[$asset]
    if (-not $expected) {
        Fail "Checksum entry for $asset not found in checksums.txt."
    }

    $actual = (Get-FileHash -Path $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        Fail "Checksum mismatch for $asset. Expected $expected, got $actual."
    }

    New-Item -ItemType Directory -Force -Path $AimaInstallDir | Out-Null
    $installPath = Join-Path $AimaInstallDir "aima.exe"
    Copy-Item -Force -Path $assetPath -Destination $installPath

    Write-Info "Installed to $installPath"
    Add-ToUserPath -Dir $AimaInstallDir
    Write-Info "Run: aima version"
} finally {
    Remove-Item -Recurse -Force -Path $tmpDir -ErrorAction SilentlyContinue
}
