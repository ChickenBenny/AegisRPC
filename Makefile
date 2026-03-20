.PHONY: up test format lint tidy

up:
	go run ./cmd/aegis-rpc/ -port 8545 -upstreams "https://eth.llamarpc.com"

test:
	go test ./... -v -race

format:
	gofmt -w .

lint:
	go vet ./...

tidy:
	go mod tidy
