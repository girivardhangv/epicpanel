.PHONY: build test run vet tidy

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

run:
	EPICPANEL_ENV=development go run ./cmd/api
