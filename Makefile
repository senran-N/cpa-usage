.PHONY: all build test clean setup dll so dylib

BINARY_NAME := cpa_usage
UNAME_S := $(shell uname -s)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

all: build

build:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(BINARY_NAME).$(PLUGIN_EXT) .

dll:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(BINARY_NAME).dll .

so:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(BINARY_NAME).so .

dylib:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(BINARY_NAME).dylib .

test:
	go test -v ./pricing/...
	go test -v ./storage/...
	go test -v ./api/...
	go test -v ./plugin/...
	@if [ -f "$(BINARY_NAME).dll" ]; then \
		go test -v -run TestDLLIntegration . ; \
	fi

setup:
	@if [ "$(OS)" = "Windows_NT" ]; then \
		scripts/setup.bat ; \
	else \
		bash scripts/setup.sh ; \
	fi

clean:
	rm -f $(BINARY_NAME).dll $(BINARY_NAME).so $(BINARY_NAME).dylib $(BINARY_NAME).h
	rm -rf data/
