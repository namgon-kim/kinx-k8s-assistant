.PHONY: build build-all build-k8s-assistant build-linux build-k8s-assistant-linux build-log-analyzer build-log-analyzer-linux build-guidance-upload build-guidance-upload-linux run run-log-analyzer run-mcp-servers tidy clean

BINARY := k8s-assistant
LOG_ANALYZER_BINARY := log-analyzer-server
GUIDANCE_UPLOAD_BINARY := guidance-upload
CMD     := ./cmd/k8s-assistant
LOG_ANALYZER_CMD := ./cmd/log-analyzer-server
GUIDANCE_UPLOAD_CMD := ./cmd/guidance-upload

tidy:
	go mod tidy

bin:
	mkdir -p bin

build: build-k8s-assistant

build-all: tidy bin
	go build -o bin/$(BINARY) $(CMD)
	go build -o bin/$(LOG_ANALYZER_BINARY) $(LOG_ANALYZER_CMD)
	go build -o bin/$(GUIDANCE_UPLOAD_BINARY) $(GUIDANCE_UPLOAD_CMD)

build-k8s-assistant: tidy bin
	go build -o bin/$(BINARY) $(CMD)

build-linux: tidy bin
	GOOS=linux GOARCH=amd64 go build -o bin/$(BINARY)-linux-amd64 $(CMD)
	GOOS=linux GOARCH=amd64 go build -o bin/$(LOG_ANALYZER_BINARY)-linux-amd64 $(LOG_ANALYZER_CMD)
	GOOS=linux GOARCH=amd64 go build -o bin/$(GUIDANCE_UPLOAD_BINARY)-linux-amd64 $(GUIDANCE_UPLOAD_CMD)

build-k8s-assistant-linux: tidy bin
	GOOS=linux GOARCH=amd64 go build -o bin/$(BINARY)-linux-amd64 $(CMD)

build-log-analyzer: tidy bin
	go build -o bin/$(LOG_ANALYZER_BINARY) $(LOG_ANALYZER_CMD)

build-log-analyzer-linux: tidy bin
	GOOS=linux GOARCH=amd64 go build -o bin/$(LOG_ANALYZER_BINARY)-linux-amd64 $(LOG_ANALYZER_CMD)

build-guidance-upload: tidy bin
	go build -o bin/$(GUIDANCE_UPLOAD_BINARY) $(GUIDANCE_UPLOAD_CMD)

build-guidance-upload-linux: tidy bin
	GOOS=linux GOARCH=amd64 go build -o bin/$(GUIDANCE_UPLOAD_BINARY)-linux-amd64 $(GUIDANCE_UPLOAD_CMD)

run: build
	./bin/$(BINARY)

run-log-analyzer: build-log-analyzer
	./bin/$(LOG_ANALYZER_BINARY)

run-mcp-servers: build-log-analyzer
	./bin/$(LOG_ANALYZER_BINARY) & LOG_PID=$$!; \
	trap 'kill $$LOG_PID 2>/dev/null || true' INT TERM EXIT; \
	wait

clean:
	rm -rf bin/
