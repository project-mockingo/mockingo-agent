.PHONY: build test fmt vet run-gateway clean

build:
	mkdir -p bin
	go build -o bin/mockingo ./cmd/mockingo
	go build -o bin/mockingo-gateway ./cmd/mockingo-gateway

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

run-gateway:
	go run ./cmd/mockingo-gateway

clean:
	go clean
