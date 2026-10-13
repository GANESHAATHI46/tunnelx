$ErrorActionPreference = 'Stop'
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'Go is not installed. Install it from https://go.dev/dl/ and reopen PowerShell.' }
go mod tidy
if ($LASTEXITCODE -ne 0) { throw 'go mod tidy failed. Check network/DNS and retry.' }
New-Item -ItemType Directory -Force -Path .\dist | Out-Null
go build -trimpath -o .\dist\tunnelx-server.exe .\cmd\server
if ($LASTEXITCODE -ne 0) { throw 'Server build failed.' }
go build -trimpath -o .\dist\tunnelx.exe .\cmd\client
if ($LASTEXITCODE -ne 0) { throw 'Client build failed.' }
Write-Host 'Build complete: dist\tunnelx-server.exe and dist\tunnelx.exe' -ForegroundColor Green
