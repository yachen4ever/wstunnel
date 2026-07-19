# crossbuild.ps1 - 在当前平台交叉编译 wstunnel 到 linux-amd64 / windows-amd64 / darwin-arm64。
#
# 用法:
#   .\crossbuild.ps1              # 编译三个平台
#   .\crossbuild.ps1 -Clean       # 清理 binaries\
#
# PowerShell 版，Windows 上方便用。等价于 crossbuild.sh。

[CmdletBinding()]
param(
    [switch]$Clean
)

$ErrorActionPreference = 'Stop'

# 三个固定目标
$Targets = @(
    @{ GOOS = 'linux';   GOARCH = 'amd64'; Ext = '' }
    @{ GOOS = 'windows'; GOARCH = 'amd64'; Ext = '.exe' }
    @{ GOOS = 'darwin';  GOARCH = 'arm64'; Ext = '' }
)

# 版本号：优先 git tag，其次 short hash，最后 dev
$Version = 'dev'
try {
    $gitDesc = git describe --tags --always --dirty 2>$null
    if ($gitDesc) { $Version = $gitDesc.Trim() }
} catch { }

$LdFlags = "-s -w -X main.version=$Version"
$OutDir = 'binaries'

if ($Clean) {
    if (Test-Path $OutDir) { Remove-Item $OutDir -Recurse -Force }
    Write-Host "cleaned $OutDir"
    exit 0
}

if (-not (Test-Path $OutDir)) {
    New-Item -ItemType Directory -Path $OutDir | Out-Null
}

Write-Host "== building wstunnel $Version =="
foreach ($t in $Targets) {
    $out = "$OutDir/wstunnel-$($t.GOOS)-$($t.GOARCH)$($t.Ext)"
    Write-Host "--> $($t.GOOS)/$($t.GOARCH) -> $out"

    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH
    $env:CGO_ENABLED = '0'

    go build -trimpath -ldflags $LdFlags -o $out .

    if (Test-Path $out) {
        $size = (Get-Item $out).Length
        Write-Host ("    size: {0:N1} KB" -f ($size / 1KB))
    }
}

# 清理环境变量，避免影响后续 go build
$env:GOOS = $null
$env:GOARCH = $null
$env:CGO_ENABLED = $null

Write-Host "== done. artifacts in $OutDir\ =="
Get-ChildItem $OutDir | ForEach-Object { Write-Host "  $($_.Name)" }
