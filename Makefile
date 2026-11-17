.PHONY: all build test install clean

PLUGIN_NAME := cpa-cache-tracker
VERSION := v0.1.5
BIN_DIR := bin

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ifeq ($(GOOS),darwin)
EXT := .dylib
else ifeq ($(GOOS),windows)
EXT := .dll
else
EXT := .so
endif

DYLIB_NAME := $(PLUGIN_NAME)-$(VERSION)$(EXT)
DYLIB_PATH := $(BIN_DIR)/$(DYLIB_NAME)
INSTALL_DIR ?= $(HOME)/.cli-proxy-api/plugins/$(GOOS)/$(GOARCH)

ifeq ($(GOARCH),arm64)
GOARM64 ?= v8.0
CGO_CFLAGS ?= -march=armv8-a
BUILD_ENV := CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM64=$(GOARM64) CGO_CFLAGS="$(CGO_CFLAGS)"
else ifeq ($(GOOS)/$(GOARCH),darwin/amd64)
CGO_CFLAGS ?= -arch x86_64
CGO_LDFLAGS ?= -arch x86_64
BUILD_ENV := CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)"
else
BUILD_ENV := CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH)
endif

all: build

test:
	go test -race -v ./...
	node --test ui/panel_*.test.cjs

build:
	mkdir -p $(BIN_DIR)
	$(BUILD_ENV) go build -buildmode=c-shared -o $(DYLIB_PATH) .

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(DYLIB_PATH) $(INSTALL_DIR)/$(DYLIB_NAME)

clean:
	rm -rf $(BIN_DIR)
