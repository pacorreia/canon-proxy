.PHONY: build test coverage lint clean

# Packages where unit tests are meaningful (no live network or camera required).
TESTABLE_PKGS := \
	./internal/config/... \
	./internal/db/... \
	./internal/store/... \
	./internal/pipeline/... \
	./internal/web/...

COVERPKG := $(shell echo $(TESTABLE_PKGS) | tr ' ' ',')

build:
	go build ./...

test:
	go test ./...

coverage:
	go test -coverprofile=coverage.out -coverpkg=$(COVERPKG) ./...
	go tool cover -func=coverage.out

coverage-html: coverage
	go tool cover -html=coverage.out

clean:
	rm -f coverage.out
