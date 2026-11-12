.PHONY: test test-race test-coverage lint fmt vet ci check-logkey tidy

## test: 运行所有单元测试
test:
	go test ./...

## test-race: 运行竞态检测测试
test-race:
	go test -race ./...

## test-coverage: 生成测试覆盖率报告
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

## lint: 运行 golangci-lint（需要先安装）
lint:
	golangci-lint run ./...

## fmt: 格式化所有 Go 文件
fmt:
	gofmt -s -w .
	goimports -w .

## vet: 运行 go vet
vet:
	go vet ./...

## check-logkey: 禁手写 "err"/"error" 字面量日志字段（AGENTS.md logx.ErrKey
## 红线，#180；错误字段名一律用 logx.ErrKey 常量，防止回潮）
check-logkey:
	@hits=$$(grep -rEn '"(err|error)",' --include='*.go' --exclude='*_test.go' . 2>/dev/null); \
	test -z "$$hits" || { echo "$$hits"; echo "发现手写 err/error 日志字段字面量，请改用 logx.ErrKey（AGENTS.md 日志红线，#180）" && exit 1; }

## ci: 模拟 CI 流程（fmt 检查 + logx.ErrKey 红线 + vet + test-race）
ci: check-logkey
	@test -z "$$(gofmt -l . | grep -v vendor | tee /dev/stderr)" || (echo "请运行 make fmt" && exit 1)
	go vet ./...
	go test -race -count=1 ./...

## tidy: 整理 go.mod
tidy:
	go mod tidy

## help: 显示帮助
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
