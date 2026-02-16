.PHONY: build dev test lint fmt generate sqlc migrate-up migrate-down docker-controller docker-worker

BINARY=opendebrid

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) .

dev:
	go run . controller

dev-worker:
	go run . worker

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w .

sqlc:
	cd internal/database && sqlc generate

generate: sqlc

migrate-up:
	go run . migrate up

migrate-down:
	go run . migrate down

docker-controller:
	docker build -f deployments/Dockerfile.controller -t opendebrid-controller .

docker-worker:
	docker build -f deployments/Dockerfile.worker -t opendebrid-worker .

clean:
	rm -f $(BINARY)
