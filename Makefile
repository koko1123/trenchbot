.PHONY: build run shadow test lint simulate ci clean backtest watcher docker-sniper

build:
	go build -o trenchbot ./cmd/sniper

run: build
	./trenchbot

shadow: build
	MODE=shadow ./trenchbot

test:
	go test -race ./...

lint:
	go vet ./...

simulate:
	go test -v -race -timeout 5m ./internal/simulation/

ci: lint test simulate build

watcher:
	go run ./cmd/watcher

backtest:
	go run ./cmd/backtest

docker-sniper:
	docker build -t trenchbot-sniper .

clean:
	rm -f trenchbot simulation-report.json
