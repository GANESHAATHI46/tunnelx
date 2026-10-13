$ErrorActionPreference = 'Stop'
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Write-Error 'Go is missing. Install from https://go.dev/dl/'; exit 1 }
go mod tidy
if ($LASTEXITCODE -ne 0) { Write-Error 'Dependency download failed. Check network/DNS.'; exit 1 }
New-Item -ItemType Directory -Force -Path .\dist | Out-Null
go build -trimpath -o .\dist\tunnelx-server.exe .\cmd\server
if ($LASTEXITCODE -ne 0) { Write-Error 'Server build failed'; exit 1 }
go build -trimpath -o .\dist\tunnelx.exe .\cmd\client
if ($LASTEXITCODE -ne 0) { Write-Error 'Client build failed'; exit 1 }
Write-Host 'Success. Binaries are in .\dist\' -ForegroundColor Green
