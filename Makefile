.PHONY: build build-linux build-log-analyzer run run-log-analyzer tidy clean

BINARY := k8s-assistant
LOG_ANALYZER_BINARY := log-analyzer-server
CMD     := ./cmd/k8s-assistant
LOG_ANALYZER_CMD := ./cmd/log-analyzer-server

tidy:
	go mod tidy

build: tidy
	go build -o bin/$(BINARY) $(CMD)

build-linux: tidy
	GOOS=linux GOARCH=amd64 go build -o bin/$(BINARY)-linux-amd64 $(CMD)

build-log-analyzer: tidy
	go build -o bin/$(LOG_ANALYZER_BINARY) $(LOG_ANALYZER_CMD)

run: build
	./bin/$(BINARY)

run-log-analyzer: build-log-analyzer
	./bin/$(LOG_ANALYZER_BINARY)

clean:
	rm -rf bin/
