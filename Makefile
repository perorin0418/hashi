VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# 使い方:
#   make build                          手元向けにビルド（bin/hashi）
#   make dist                           Linux / Mac / Windows 向けにビルド（dist/）
#   make image ECR=<ecr_repository_url> イメージをビルドして ECR へ push（要 docker、aws でログイン済み）
#   make release TAG=v0.2.0             タグを付けてプッシュし、各 OS 向けにビルドして GitHub のリリースを作る（要 gh）

.PHONY: build dist test image release

build:
	go build -ldflags '$(LDFLAGS)' -o bin/hashi ./cmd/hashi

test:
	go vet ./... && go test ./...

dist:
	rm -rf dist && mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-linux-amd64       ./cmd/hashi
	GOOS=linux   GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-linux-arm64       ./cmd/hashi
	GOOS=darwin  GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-darwin-amd64      ./cmd/hashi
	GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-darwin-arm64      ./cmd/hashi
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/hashi-windows-amd64.exe ./cmd/hashi
	cd dist && sha256sum hashi-* > checksums.txt

PLATFORM ?= linux/amd64
image:
	@test -n "$(ECR)" || (echo "ECR=<ecr_repository_url> を指定してください" && exit 1)
	aws ecr get-login-password | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(ECR)))
	docker build --platform $(PLATFORM) -t $(ECR):latest image
	docker push $(ECR):latest

release:
	@test -n "$(TAG)" || (echo "TAG=v0.2.0 のように指定してください" && exit 1)
	@test -z "$$(git status --porcelain)" || (echo "コミットしていない変更があります" && exit 1)
	go vet ./... && go test ./...
	git tag $(TAG)
	git push origin $(TAG)
	$(MAKE) dist VERSION=$(TAG)
	gh release create $(TAG) dist/* --title $(TAG) --generate-notes
