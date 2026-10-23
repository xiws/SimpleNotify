APP     := notify
VERSION ?= 0.1.0
GOFLAGS ?=
LDFLAGS := -s -w -X github.com/xiws/SimpleNotify/internal/cli.version=$(VERSION)

.PHONY: build run test vet fmt tidy clean

build:
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(APP) ./cmd/notify

run: build
	./$(APP) serve --config conf.json

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

tidy:
	go mod tidy

clean:
	rm -f $(APP)
