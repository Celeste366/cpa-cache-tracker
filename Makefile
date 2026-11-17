.PHONY: all build test install clean

PLUGIN_NAME := cpa-cache-tracker
VERSION := v0.1.2
BIN_DIR := bin
DYLIB_NAME := $(PLUGIN_NAME)-$(VERSION).dylib
DYLIB_PATH := $(BIN_DIR)/$(DYLIB_NAME)
INSTALL_DIR := $(HOME)/.cli-proxy-api/plugins/darwin/arm64

GOARM64 ?= v8.0
CGO_CFLAGS ?= -march=armv8-a

all: build

test:
	go test -race -v ./...
	node --test ui/panel_auth.test.cjs

build:
	mkdir -p $(BIN_DIR)
	GOARM64=$(GOARM64) CGO_CFLAGS="$(CGO_CFLAGS)" go build -buildmode=c-shared -o $(DYLIB_PATH) .

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(DYLIB_PATH) $(INSTALL_DIR)/$(DYLIB_NAME)

clean:
	rm -rf $(BIN_DIR)
