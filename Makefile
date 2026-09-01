# AI-SCRM 工程命令合集（2026-09-01）
# 依赖：go 1.25、node >= 20（前端构建用）
# 测试说明：内部依赖 DB 的单测在未起 PG 时自动跳过（testutil.SetupTestDB 内 t.Skip）

.PHONY: build vet fmt fmt-check test test-short frontend-build smoke all

all: fmt-check vet test build

# 编译
build:
	go build -o ai-scrm ./cmd/server

# 静态检查
vet:
	go vet ./...

# 格式化（直接改文件）
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*' -not -path './frontend-react/*')

# 格式化检查（CI 用，不改文件，未格式化即失败）
fmt-check:
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*' -not -path './frontend-react/*')); \
	if [ -n "$$unformatted" ]; then echo "以下文件未 gofmt:"; echo "$$unformatted"; exit 1; fi
	@echo "gofmt OK"

# 全部单测（DB 依赖用例在无 PG 时自动跳过）
test:
	go test ./...

# 快速单测：跳过 DB 依赖用例（无需 PG）
test-short:
	go test -short ./...

# 前端构建（产物 frontend-react/dist 由 main.go 以 SPA 托管）
frontend-build:
	cd frontend-react && npm ci && npm run build

# 冒烟三件套（需服务已启动）
smoke:
	./tools/smoke.sh 9090
	./tools/smoke_org.sh 9090
	./tools/uat.sh 9090