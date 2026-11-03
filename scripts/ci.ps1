# 与 GitHub Actions 保持一致的本地检查口径。
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

Write-Host '[1/4] go vet ./...'
go vet ./...

Write-Host '[2/4] go build ./...'
go build ./...

Write-Host '[3/4] go test ./...'
go test ./...

Write-Host '[4/4] web build'
Push-Location web
npm ci --no-audit --no-fund
npm run build
Pop-Location

Write-Host 'CI checks passed'
