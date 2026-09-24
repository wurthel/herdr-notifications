BINARY := herdr-notifications
PLUGIN_ID := herdr-notifications

.DEFAULT_GOAL := build
.PHONY: build test vet fmt lint link unlink clean

build:
	go build -trimpath -ldflags="-s -w" -o $(BINARY) .

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
	go vet ./...

link: build
	herdr plugin link .

unlink:
	herdr plugin unlink $(PLUGIN_ID)

clean:
	rm -f $(BINARY) coverage.out coverage.html
