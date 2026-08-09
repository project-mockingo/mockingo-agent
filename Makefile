.PHONY: build test race fmt vet cross-build clean

build:
	mkdir -p bin
	go build -trimpath -o bin/mockingo ./cmd/mockingo

test:
	go test ./...

race:
	go test -race ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

vet:
	go vet ./...

cross-build:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -o dist/mockingo-windows-amd64.exe ./cmd/mockingo
	GOOS=linux GOARCH=amd64 go build -trimpath -o dist/mockingo-linux-amd64 ./cmd/mockingo
	GOOS=linux GOARCH=arm64 go build -trimpath -o dist/mockingo-linux-arm64 ./cmd/mockingo
	GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/mockingo-darwin-amd64 ./cmd/mockingo
	GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/mockingo-darwin-arm64 ./cmd/mockingo

clean:
	go clean
