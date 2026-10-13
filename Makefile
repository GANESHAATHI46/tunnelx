.PHONY: tidy fmt test vet build

tidy:
	go mod tidy
fmt:
	gofmt -w cmd

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./cmd/server ./cmd/client
