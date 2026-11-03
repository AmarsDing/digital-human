SHELL := /bin/bash
KRATOS_TP := $(shell go env GOMODCACHE)/github.com/go-kratos/kratos/v2@v2.9.2/third_party
MODULE := github.com/teamwork/digital-human
PROTOS := api/live/v1/live.proto api/live/v1/danmaku.proto api/config/v1/settings.proto

.PHONY: all proto ent wire build run test vet fmt web-install web-build ci clean

all: build

# 生成 proto（需要 protoc、protoc-gen-go、protoc-gen-go-http）
proto:
	protoc -I api -I "$(KRATOS_TP)" \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-http_out=. --go-http_opt=module=$(MODULE) \
		$(PROTOS)

# 生成 Ent（schema 变更后执行）
ent:
	go run entgo.io/ent/cmd/ent generate ./internal/data/ent/schema

# 生成 Wire（provider 变更后执行）
wire:
	go run github.com/google/wire/cmd/wire ./cmd/dh

build:
	go build -o bin/dh.exe ./cmd/dh

run: build
	./bin/dh.exe -conf configs/config.yaml

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w api cmd internal pkg

# fmt-check 任何未格式化文件都会失败并列出文件名。
fmt-check:
	@bad=$$(gofmt -l .); if [ -n "$$bad" ]; then echo "gofmt 未通过: $$bad"; exit 1; fi

test-race:
	go test -race -count=1 ./internal/... ./pkg/...

web-install:
	cd web && npm install

web-build:
	cd web && npm run build

# CI 与本地一致的检查口径
ci: fmt-check vet test-race web-build

clean:
	rm -rf bin web/dist
