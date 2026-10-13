#!/usr/bin/env sh
set -eu
go mod tidy
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/tunnelx-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/tunnelx-linux ./cmd/client
printf 'Build complete: dist/tunnelx-server and dist/tunnelx-linux\n'
