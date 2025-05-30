GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

FILE_PATTERN := 'yaml\|html\|go\|sql\|Makefile\|js\|css\|scss'

dev_server:
	find . | grep $(FILE_PATTERN) | GO_ENV=dev entr -c -r go run main.go 3000

watch_test:
	find . | grep $(FILE_PATTERN) | entr -c go test ./pkg/...

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -o bin/redirector main.go

clean:
	rm -rf bin

