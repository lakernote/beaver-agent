.PHONY: serve api dev build build-linux web test run container container-test clean

LINUX_ARCH ?= amd64
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_FLAGS ?= -trimpath -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"
IMAGE ?= beaver:local

# 本地开发一条命令：增量构建前端（不跑 npm ci）并仅监听 127.0.0.1:8080，数据在 data/beaver.db。
serve:
	cd web && ([ -d node_modules ] || npm ci) && npm run build
	go run ./cmd/beaver -db data/beaver.db -listen 127.0.0.1:8080

# 热更新开发的后端：需要 web/dist 已存在；另开终端执行 `make dev` 获得 :5173 前端热更新。
api:
	@[ -d web/dist ] || (cd web && npm ci && npm run build)
	go run ./cmd/beaver -db data/beaver.db -listen 127.0.0.1:8080

# 前端热更新开发服务（:5173，/api 代理到 :8080）；需另开终端运行后端。
dev:
	cd web && npm run dev

# 默认发布构建：先生成前端 dist，再把 dist 嵌入 Go 单二进制。
build: web
	mkdir -p bin
	go build $(BUILD_FLAGS) -o bin/beaver ./cmd/beaver

# 交叉编译 Linux 单文件；可用 `make build-linux LINUX_ARCH=arm64` 切换架构。
build-linux: web
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build $(BUILD_FLAGS) -o bin/beaver-linux-$(LINUX_ARCH) ./cmd/beaver

# npm ci 严格按照 package-lock.json 安装依赖，适合 CI 和可复现构建。
web:
	cd web && npm ci && VITE_APP_VERSION=$(VERSION) npm run build

# 本项目的完整本地检查：依赖校验、Go 静态检查、竞态测试和前端构建。
test:
	cd web && npm ci && npm test && npm run build
	go mod verify
	go vet ./...
	go test -race ./...

# build 成功后以前台方式启动；默认监听 0.0.0.0:8080。
run: build
	./bin/beaver

# 构建与发布环境相同的非 root 容器镜像。
container:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --tag $(IMAGE) .

# 验证镜像启动、健康接口、非 root 身份以及 /data 跨容器持久化。
container-test: container
	bash .github/scripts/container-smoke.sh $(IMAGE)

# 删除可重新生成的构建产物，不会删除 SQLite 数据目录。
clean:
	rm -rf bin web/dist
