# agent-issues — local issue ledger

default:
    @just --list

build:
    go build -o issues ./cmd/issues

install:
    go install ./cmd/issues

test:
    go test ./...

lint:
    go vet ./...

format:
    gofmt -w .

check: format lint test
