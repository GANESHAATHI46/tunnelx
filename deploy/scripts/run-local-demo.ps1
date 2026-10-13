# Run from the repository root in PowerShell. Start this script in terminal 1.
$ErrorActionPreference = 'Stop'
if (-not $env:TUNNELX_TOKEN -or $env:TUNNELX_TOKEN.Length -lt 32) { $env:TUNNELX_TOKEN = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N') }
if (-not $env:TUNNELX_ADMIN_USER) { $env:TUNNELX_ADMIN_USER = 'admin' }
if (-not $env:TUNNELX_ADMIN_PASS) { $env:TUNNELX_ADMIN_PASS = [guid]::NewGuid().ToString('N') + '!' }
$env:TUNNELX_LISTEN = '127.0.0.1:8080'
$env:TUNNELX_DATA_DIR = '.\data'
Write-Host "Admin user: $env:TUNNELX_ADMIN_USER"
Write-Host "Admin password (save it): $env:TUNNELX_ADMIN_PASS"
Write-Host "Tunnel token (use in client terminal): $env:TUNNELX_TOKEN"
go run .\cmd\server
