.PHONY: run cli build test docker-run lint tidy

# Run Slack bot (production mode)
run:
	go run ./cmd/bot

# Run local CLI REPL for testing (no Slack tokens needed)
cli:
	go run ./cmd/bot -mode=cli

# Build binary to bin/bot
build:
	go build -o bin/bot ./cmd/bot

# Run tests
test:
	go test ./...

# Build and run with Docker
docker-run:
	docker compose up --build

# Lint
lint:
	golangci-lint run

# Tidy dependencies
tidy:
	go mod tidy
