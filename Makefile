APP     := gori
VERSION := 0.1.0
LDFLAGS := -s -w -X github.com/leelsey/gori.Version=$(VERSION)
BUILD   := CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)'
BIN     := bin
DIST    := dist
PKG     := ./cmd/gori

PLATFORMS := \
	darwin/amd64 \
	darwin/arm64 \
	linux/amd64 \
	linux/arm64 \
	windows/amd64 \
	windows/arm64

.PHONY: all build clean test release $(PLATFORMS)

all: build

build:
	$(BUILD) -o $(BIN)/$(APP) $(PKG)

test:
	go test -v ./...

clean:
	rm -rf $(BIN) $(DIST)

release: $(PLATFORMS)
	@cd $(DIST) && shasum -a 256 $(APP)-* > checksums.txt
	@echo "release binaries in $(DIST)/"

$(PLATFORMS):
	$(eval OS := $(word 1,$(subst /, ,$@)))
	$(eval ARCH := $(word 2,$(subst /, ,$@)))
	$(eval EXT := $(if $(filter windows,$(OS)),.exe,))
	GOOS=$(OS) GOARCH=$(ARCH) $(BUILD) -o $(DIST)/$(APP)-$(OS)-$(ARCH)$(EXT) $(PKG)
