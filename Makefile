.PHONY: build dev dev-worker test lint fmt generate sqlc proto migrate-up migrate-down docker clean

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
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...

fmt:
	gofmt -w .
	go run golang.org/x/tools/cmd/goimports@latest -w .

sqlc:
	cd internal/database && sqlc generate

proto:
	protoc --go_out=. --go_opt=module=github.com/viperadnan-git/opendebrid \
		--go-grpc_out=. --go-grpc_opt=module=github.com/viperadnan-git/opendebrid \
		proto/opendebrid/*.proto

generate: sqlc proto

migrate-up:
	go run . migrate up

migrate-down:
	go run . migrate down

docker:
	docker build -t opendebrid .

clean:
	rm -f $(BINARY)
