.PHONY: lint fmt test test-integration build

lint:
	go vet ./...
	@files=$$(gofmt -l . 2>/dev/null); if [ -n "$$files" ]; then echo "ERROR: gofmt drift detected, run 'make fmt'"; echo "$$files"; exit 1; fi

# 自动修复格式（lint 只检出不修；提交前跑一次）
fmt:
	gofmt -w .

test:
	go test ./...

# 集成测试（真 PG，testcontainers 自起容器；需本机 Docker）
test-integration:
	go test -tags integration ./...

build:
	go build -trimpath -o bin/apiserver ./cmd/apiserver
