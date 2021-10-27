SHELL=/bin/bash

BIN=bin/crawley
SRC=./cmd/crawley
COP=test.coverage

GIT_HASH=`git rev-parse --short HEAD`
BUILD_AT=`date +%FT%T%z`

LDFLAGS=-w -s -X main.GitHash=${GIT_HASH} -X main.BuildDate=${BUILD_AT}

.PHONY: build

build: lint
	go build -ldflags "${LDFLAGS}" -o "${BIN}" "${SRC}"

vet:
	go vet ./...

lint: vet
	golangci-lint run

test: vet
	go test -race -count 1 -v -tags=test -coverprofile="${COP}" ./...

test-cover: test
	go tool cover -func="${COP}"

clean:
	[ -f "${BIN}" ] && rm "${BIN}"
	[ -f "${COP}" ] && rm "${COP}"