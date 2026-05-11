.PHONY: build build-linux build-log-analyzer build-trouble-shooting run run-log-analyzer run-trouble-shooting tidy clean

BINARY := k8s-assistant
LOG_ANALYZER_BINARY := log-analyzer-server
TROUBLE_SHOOTING_BINARY := trouble-shooting-server
CMD     := ./cmd/k8s-assistant
LOG_ANALYZER_CMD := ./cmd/log-analyzer-server
TROUBLE_SHOOTING_CMD := ./cmd/trouble-shooting-server

tidy:
	go mod tidy

build: tidy
	go build -o bin/$(BINARY) $(CMD)

build-linux: tidy
	GOOS=linux GOARCH=amd64 go build -o bin/$(BINARY)-linux-amd64 $(CMD)

build-log-analyzer: tidy
	go build -o bin/$(LOG_ANALYZER_BINARY) $(LOG_ANALYZER_CMD)

build-trouble-shooting: tidy
	go build -o bin/$(TROUBLE_SHOOTING_BINARY) $(TROUBLE_SHOOTING_CMD)

run: build
	./bin/$(BINARY)

run-log-analyzer: build-log-analyzer
	./bin/$(LOG_ANALYZER_BINARY)

run-trouble-shooting: build-trouble-shooting
	./bin/$(TROUBLE_SHOOTING_BINARY)

clean:
	rm -rf bin/
