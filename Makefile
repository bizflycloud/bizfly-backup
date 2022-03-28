PROJECT_NAME := "bizfly-backup"
PKG := "github.com/bizflycloud/$(PROJECT_NAME)"
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/ | grep -v _test.go)
BIZFLY_BACKUP_VERSION ?= "dev"
GIT_COMMIT := $(shell git rev-parse --short HEAD)
BUILD_TIME := $(shell date --utc +%FT%T%Z)

.PHONY: all dep lint vet test test-coverage build clean

all: build

dep: ## Get the dependencies
	@go mod download

lint: ## Lint Golang files
	@golint -set_exit_status ${PKG_LIST}

vet: ## Run go vet
	@go vet ${PKG_LIST}

test: ## Run unittests
	@go test -short ${PKG_LIST}

test-coverage: ## Run tests with coverage
	@go test -short -coverprofile cover.out -covermode=atomic ${PKG_LIST}
	@cat cover.out >> coverage.txt

build: dep ## Build the binary file
	@go build -ldflags="-X $(PKG)/pkg/agentversion.CurrentVersion=$(BIZFLY_BACKUP_VERSION) -X $(PKG)/pkg/agentversion.GitCommit=${GIT_COMMIT} -X $(PKG)/pkg/agentversion.BuildTime=${BUILD_TIME}" -o build/main main.go
	## $(PKG)

clean: ## Remove previous build
	@rm -f $(PROJECT_NAME)/build

help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
