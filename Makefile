VERSION ?= $(shell date +%Y%m%d)
LDFLAGS := -s -w -X main.version=$(VERSION)

# 使い方:
#   make build                          手元向けにビルド（bin/hashi）
#   make dist                           Linux / Mac / Windows 向けにビルド（dist/）
#   make image ECR=<ecr_repository_url> イメージをビルドして ECR へ push（要 docker、aws でログイン済み）

.PHONY: build dist test image

build:
	go build -ldflags '$(LDFLAGS)' -o bin/hashi ./cmd/hashi

test:
	go vet ./... && go test ./...

dist:
	GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-linux-amd64       ./cmd/hashi
	GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-darwin-arm64      ./cmd/hashi
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-windows-amd64.exe ./cmd/hashi

PLATFORM ?= linux/amd64
image:
	@test -n "$(ECR)" || (echo "ECR=<ecr_repository_url> を指定してください" && exit 1)
	aws ecr get-login-password | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(ECR)))
	docker build --platform $(PLATFORM) -t $(ECR):latest image
	docker push $(ECR):latest
