.PHONY: build test test-integration fmt vet migrate docker-build compose-config compose-up compose-down cross-build clean

build:
	mkdir -p bin
	go build -o bin/mockingo ./cmd/mockingo
	go build -o bin/mockingo-gateway ./cmd/mockingo-gateway

test:
	go test ./...

test-integration:
	go test -tags=integration ./internal/integration/...

fmt:
	gofmt -w $$(find cmd internal pkg -name '*.go')

vet:
	go vet ./...

migrate:
	go run ./cmd/mockingo-gateway migrate

docker-build:
	docker build -f deploy/Dockerfile.gateway -t mockingo-gateway:stage2a .
	docker build -f deploy/Dockerfile.caddy -t mockingo-caddy:stage2a .

compose-config:
	docker compose --env-file deploy/.env -f deploy/docker-compose.production.yml config --quiet

compose-up:
	docker compose --env-file deploy/.env -f deploy/docker-compose.production.yml up -d

compose-down:
	docker compose --env-file deploy/.env -f deploy/docker-compose.production.yml down

cross-build:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -o dist/mockingo-windows-amd64.exe ./cmd/mockingo
	GOOS=linux GOARCH=amd64 go build -trimpath -o dist/mockingo-linux-amd64 ./cmd/mockingo
	GOOS=linux GOARCH=arm64 go build -trimpath -o dist/mockingo-linux-arm64 ./cmd/mockingo
	GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/mockingo-darwin-amd64 ./cmd/mockingo
	GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/mockingo-darwin-arm64 ./cmd/mockingo

clean:
	go clean
