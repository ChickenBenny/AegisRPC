.PHONY: up compose-up compose-down test format lint tidy

up:
	go run ./cmd/aegis-rpc/ -port 8545 -upstreams "https://eth.llamarpc.com"

compose-up:
	docker compose up --build

compose-down:
	docker compose down

test:
	go test ./... -v -race

format:
	gofmt -w .

lint:
	go vet ./...

tidy:
	go mod tidy
