.PHONY: build run test clean demo deps

BINARY := ./bin/hotreload
TEST_BINARY := ./testserver/bin/server

## deps: Download module dependencies
deps:
	go mod download
	go mod tidy

## build: Build the hotreload binary
build: deps
	@mkdir -p ./bin
	go build -o $(BINARY) ./cmd/hotreload
	@echo "Built: $(BINARY)"

## test: Run all tests
test:
	go test ./... -v -timeout 30s

## test-short: Run tests without long integration tests
test-short:
	go test ./... -short -timeout 15s

## build-testserver: Build the demo test server
build-testserver:
	@mkdir -p ./testserver/bin
	go build -o $(TEST_BINARY) ./testserver/cmd/server
	@echo "Built: $(TEST_BINARY)"

## demo: Build hotreload and run it against the testserver
demo: build
	@echo ""
	@echo "==> Starting hotreload demo"
	@echo "==> Edit testserver/cmd/server/main.go to trigger a reload"
	@echo "==> Press Ctrl+C to stop"
	@echo ""
	$(BINARY) \
		--root ./testserver \
		--build "sh -c 'cd ./testserver && go build -o ./bin/server ./cmd/server'" \
		--exec "./testserver/bin/server" \
		--debounce 300

## demo-verbose: Same as demo but with debug logging
demo-verbose: build
	$(BINARY) \
		--root ./testserver \
		--build "go build -o ./testserver/bin/server ./testserver/cmd/server" \
		--exec "./testserver/bin/server" \
		--debounce 300 \
		--verbose

## clean: Remove build artifacts
clean:
	rm -rf ./bin ./testserver/bin

## install: Install hotreload to $GOPATH/bin
install:
	go install ./cmd/hotreload

## lint: Run go vet
lint:
	go vet ./...

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //'
