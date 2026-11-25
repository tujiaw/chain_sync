$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
$savedEnv = @{}
foreach ($key in @('CGO_ENABLED','GOOS','GOARCH','GOAMD64','GOARM64')) {
    $savedEnv[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOAMD64 = 'v1'
    $env:GOARM64 = 'v8.0'
    New-Item -ItemType Directory -Force dist | Out-Null
    foreach ($arch in @('amd64','arm64')) {
        $env:GOARCH = $arch
        $package = "chainsync-linux-$arch"
        $folder = Join-Path 'dist' $package
        New-Item -ItemType Directory -Force $folder | Out-Null
        go build -trimpath -ldflags '-s -w' -o "$folder/chainsync" .
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $arch" }
        Copy-Item -LiteralPath README.md -Destination $folder
        tar -czf "dist/$package.tar.gz" -C dist "$package/chainsync" "$package/README.md"
        if ($LASTEXITCODE -ne 0) { throw "Package failed: $arch" }
    }
    $lines = Get-ChildItem dist/chainsync-linux-*.tar.gz | ForEach-Object {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        "$hash  $($_.Name)"
    }
    [IO.File]::WriteAllLines((Join-Path $PSScriptRoot 'dist/SHA256SUMS'), [string[]]$lines, [Text.UTF8Encoding]::new($false))
} finally {
    foreach ($key in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($key, $savedEnv[$key], 'Process') }
    Pop-Location
}
