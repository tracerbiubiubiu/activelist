.PHONY: lint test test-integration build

lint:
	go vet ./...
	@files=$$(gofmt -l . 2>/dev/null); if [ -n "$$files" ]; then echo "ERROR: gofmt drift detected, run 'gofmt -w .'"; echo "$$files"; exit 1; fi

test:
	go test ./...

# 集成测试（真 PG，testcontainers 自起容器；需本机 Docker）
test-integration:
	go test -tags integration ./...

build:
	go build -trimpath -o bin/apiserver ./cmd/apiserver
