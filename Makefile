.PHONY: help check test build build-arm64 build-arm64-headless build-amd64-headless build-all desktop-assets package-darwin wails-cross web frontend tizen-wgt validate-tizen-wgt smoke-tizen-engine deploy-pi bootstrap-server-dry-run

VERSION ?= $(shell tr -d '[:space:]' < VERSION)
PI_HOST ?=
TIZEN_VERSION ?= $(VERSION)
TIZEN_TARGET ?= 7.0
TIZEN_WGT := clients/tizen/.build/artifacts/FileListTV-$(TIZEN_VERSION).wgt
GO_CACHE ?= /tmp/filelist-streaming-go-cache
GO_LDFLAGS := -s -w -X github.com/mihaiflorentin88/filelist-streaming-service/internal/composition.Version=$(VERSION)

# Desktop GUI tooling. wails3 (v3.0.0-beta.16) drives the darwin builds and
# packaging plus icon/syso generation (Taskfiles: Taskfile.yml, build/).
WAILS3 ?= wails3
# GOMODCACHE bind-mount flags for the wails-cross containers (wails3 tool
# docker-mounts); keeps the cross builds off the network for vendored modules.
WAILS_DOCKER_MOUNTS := $(shell $(WAILS3) tool docker-mounts)

## check: run Go tests, Python tools tests, go vet, and whitespace checks
check:
	GOCACHE="$(GO_CACHE)" go test ./...
	python3 -m unittest discover -s tools/tests -p 'test_*.py'
	GOCACHE="$(GO_CACHE)" go vet ./...
	git diff --check

## test: run Go tests with the race detector and Python tools tests
test:
	GOCACHE="$(GO_CACHE)" go test -race ./...
	python3 -m unittest discover -s tools/tests -p 'test_*.py'

# Host-native cgo GUI build (macOS host -> darwin/arm64). Requires: wails3
# (icons + build task) and Docker (web + desktop-assets prerequisites).
# The binary embeds both UIs: internal/gui/static (desktop) and
# internal/adapters/httpapi/static (browser), so both asset builds run first.
## build: host darwin/arm64 cgo GUI binary -> bin/filelist-streaming (both UIs embedded)
build: web desktop-assets
	GOCACHE="$(GO_CACHE)" $(WAILS3) build GO_LDFLAGS="$(GO_LDFLAGS)"

# Desktop GUI frontend (Preact) -> internal/gui/static, dockerized exactly
# like `web` (same image, workspace-scoped npm script). Requires Docker.
## desktop-assets: build the desktop GUI frontend -> internal/gui/static (Docker required)
desktop-assets:
	docker build -f deploy/docker/Dockerfile.frontend -t filelist-frontend-build .
	docker run --rm --user "$(shell id -u):$(shell id -g)" -v "$(CURDIR):/src" -v /src/node_modules -v /src/clients/tv/node_modules filelist-frontend-build npm run build:desktop

# macOS .app bundle (make build-all first => universal arm64+amd64 via lipo).
# With only one slice present it rebuilds and bundles the host-arch binary.
# Output: bin/FileList Streaming.app (build/darwin/Info.plist metadata).
## package-darwin: macOS .app bundle -> bin/FileList Streaming.app (make build-all first for universal)
package-darwin: web desktop-assets
	@if [ -f bin/filelist-streaming-darwin-arm64 ] && [ -f bin/filelist-streaming-darwin-amd64 ]; then \
		echo ">> Universal .app: both darwin arch slices present, merging with lipo"; \
		lipo -create -output bin/filelist-streaming \
			bin/filelist-streaming-darwin-arm64 bin/filelist-streaming-darwin-amd64; \
		GOCACHE="$(GO_CACHE)" $(WAILS3) task darwin:package:existing BUNDLE_NAME="FileList Streaming"; \
	else \
		GOCACHE="$(GO_CACHE)" $(WAILS3) package BUNDLE_NAME="FileList Streaming" GO_LDFLAGS="$(GO_LDFLAGS)"; \
	fi

# Builds the wails-cross images used by the linux GUI cross-builds below.
# First run pulls/builds ~800MB+ per platform; afterwards Docker's layer
# cache makes this a no-op. Equivalent to running `wails3 task setup:docker`
# once (host arch) plus an amd64-variant image for the emulated amd64 slice.
## wails-cross: build the wails-cross Docker images used by the linux GUI cross-builds
wails-cross:
	$(WAILS3) task setup:docker
	docker build --platform linux/amd64 -t wails-cross:amd64 -f build/docker/Dockerfile.cross build/docker/

# Linux/arm64 GUI binary via the wails Docker cross toolchain. This is the
# exact invocation Task 15's pi-deploy depends on — do not paraphrase it.
# Requires: web + desktop-assets (embedded UIs), the wails-cross image
# (order-only `wails-cross` prerequisite; `wails3 task setup:docker` once),
# wails3 (docker-mounts), and an arm64-capable Docker host. The container
# compiles natively with gcc against gtk3/webkit2gtk-4.1 (the `gtk3` tag —
# what Raspberry Pi OS ships); production drops the wails dev tools.
## build-arm64: linux/arm64 GUI binary -> bin/filelist-streaming-linux-arm64 (Docker wails-cross)
build-arm64: web desktop-assets | wails-cross
	docker run --rm -v "$(CURDIR):/app" -w /app $(WAILS_DOCKER_MOUNTS) \
		-v filelist-go-build-linux-arm64:/root/.cache/go-build \
		-e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=arm64 -e CC=gcc \
		--entrypoint go wails-cross build \
		-tags production,gtk3 -trimpath -buildvcs=false \
		-ldflags="$(GO_LDFLAGS)" \
		-o bin/filelist-streaming-linux-arm64 ./cmd/server

# Pure headless arm64 build (no cgo, no webkit2gtk). Ubuntu 22.04/Raspbian
# Bullseye lack libwebkit2gtk-4.1, and headless systemd units never open a
# window — the `headless` tag compiles internal/gui down to the
# ErrNoDisplay fallback, so the binary is fully static. Use with
# deploy/systemd/filelist-streaming.service (serve mode).
## build-arm64-headless: pure headless linux/arm64 binary -> bin/filelist-streaming-linux-arm64-headless (no cgo)
build-arm64-headless: web
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags headless -trimpath -buildvcs=false \
		-ldflags="$(GO_LDFLAGS)" \
		-o bin/filelist-streaming-linux-arm64-headless ./cmd/server

# Pure headless amd64 build, same recipe as build-arm64-headless: no cgo,
# no webkit2gtk; the `headless` tag compiles internal/gui down to the
# ErrNoDisplay fallback so the binary is fully static. Matches the
# linux-amd64-headless release matrix entry.
## build-amd64-headless: pure headless linux/amd64 binary -> bin/filelist-streaming-linux-amd64-headless (no cgo)
build-amd64-headless: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags headless -trimpath -buildvcs=false \
		-ldflags="$(GO_LDFLAGS)" \
		-o bin/filelist-streaming-linux-amd64-headless ./cmd/server

# Seven release binaries, then the universal macOS .app (this file's own
# host flow: on a macOS host the recipe ends by packaging
# bin/"FileList Streaming.app" from the two darwin slices via lipo).
#   - all: web + desktop-assets once (embedded UIs), wails3 on PATH.
#   - windows amd64/arm64: cgo-free; icon/version resources via
#     cmd/server/wails_windows_<arch>.syso (wails3 generate syso, generated
#     and removed per build, git-ignored), GUI subsystem via -H windowsgui.
#   - darwin arm64: native on a macOS host (wails3 task darwin:build).
#     darwin amd64: native cross via clang -arch x86_64.
#   - linux amd64/arm64 GUI: docker wails-cross containers (same invocation
#     shape as build-arm64); amd64 needs --platform linux/amd64 (emulated on
#     arm64 hosts — expect a long build).
#   - linux armv7: pure headless (CGO_ENABLED=0; internal/gui compiles to
#     the ErrNoDisplay fallback via build tags, no webkit2gtk needed).
## build-all: seven release binaries + universal macOS .app -> bin/ (Docker + wails3; macOS host for the .app)
build-all: web desktop-assets | wails-cross
	GOCACHE="$(GO_CACHE)" $(WAILS3) task windows:build ARCH=amd64 OUTPUT=bin/filelist-streaming-windows-amd64.exe GO_LDFLAGS="$(GO_LDFLAGS)"
	GOCACHE="$(GO_CACHE)" $(WAILS3) task windows:build ARCH=arm64 OUTPUT=bin/filelist-streaming-windows-arm64.exe GO_LDFLAGS="$(GO_LDFLAGS)"
	GOCACHE="$(GO_CACHE)" $(WAILS3) task darwin:build ARCH=arm64 OUTPUT=bin/filelist-streaming-darwin-arm64 GO_LDFLAGS="$(GO_LDFLAGS)"
	GOCACHE="$(GO_CACHE)" $(WAILS3) task darwin:build ARCH=amd64 CGO_FLAGS="-arch x86_64 -mmacosx-version-min=11.0" OUTPUT=bin/filelist-streaming-darwin-amd64 GO_LDFLAGS="$(GO_LDFLAGS)"
	docker run --rm -v "$(CURDIR):/app" -w /app $(WAILS_DOCKER_MOUNTS) \
		-v filelist-go-build-linux-arm64:/root/.cache/go-build \
		-e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=arm64 -e CC=gcc \
		--entrypoint go wails-cross build \
		-tags production,gtk3 -trimpath -buildvcs=false \
		-ldflags="$(GO_LDFLAGS)" \
		-o bin/filelist-streaming-linux-arm64 ./cmd/server
	docker run --rm --platform linux/amd64 -v "$(CURDIR):/app" -w /app $(WAILS_DOCKER_MOUNTS) \
		-v filelist-go-build-linux-amd64:/root/.cache/go-build \
		-e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 -e CC=gcc \
		--entrypoint go wails-cross:amd64 build \
		-tags production,gtk3 -trimpath -buildvcs=false \
		-ldflags="$(GO_LDFLAGS)" \
		-o bin/filelist-streaming-linux-amd64 ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-linux-armv7 ./cmd/server
	@if [ "$$(uname -s)" = "Darwin" ]; then $(MAKE) package-darwin; fi

# web refreshes the browser build that `go:embed static/*` freezes into the
# server binary, so the build* targets never ship a stale UI. It runs the
# same pinned node:24 image as `frontend` but only builds @filelist/web; the
# Tizen client and WGT packing stay in `frontend`. static/ is git-ignored.
## web: build the browser UI -> internal/adapters/httpapi/static (Docker required)
web:
	docker build -f deploy/docker/Dockerfile.frontend -t filelist-frontend-build .
	docker run --rm --user "$(shell id -u):$(shell id -g)" -v "$(CURDIR):/src" -v /src/node_modules -v /src/clients/tv/node_modules filelist-frontend-build npm run build:web

## frontend: build browser + Tizen clients, then pack the WGT (Docker required)
frontend:
	docker build -f deploy/docker/Dockerfile.frontend -t filelist-frontend-build .
	docker run --rm --user "$(shell id -u):$(shell id -g)" -v "$(CURDIR):/src" -v /src/node_modules -v /src/clients/tv/node_modules filelist-frontend-build
	$(MAKE) tizen-wgt

## tizen-wgt: pack the unsigned Tizen TV app -> clients/tizen/.build/artifacts/FileListTV-$(TIZEN_VERSION).wgt
tizen-wgt:
	python3 tools/tizen_wgt.py pack \
		--source clients/tv/dist \
		--config clients/tizen/config.xml \
		--icon clients/tizen/icon.png \
		--output "$(TIZEN_WGT)" \
		--target-tizen "$(TIZEN_TARGET)"

## validate-tizen-wgt: validate the packed WGT against the Tizen support floor
validate-tizen-wgt:
	python3 tools/tizen_wgt.py validate \
		--file "$(TIZEN_WGT)" \
		--target-tizen "$(TIZEN_TARGET)"

# Headless old-engine boot smoke (ticket #84, parent #79): boots the real
# clients/tv/dist in the pinned oldest reliably obtainable old Chromium,
# selenoid/chrome:63.0 = Google Chrome 63.0.3239.84 — the Tizen 5.0-era engine
# floor ("Tizen 5.0-era Chromium 63", clients/tv/vite.config.ts). Selenoid
# (Aerokube) still publishes the tag on Docker Hub. The pin is the guarantee's
# ceiling: nothing older than Chrome 63 is covered. Requires Docker and
# Node >= 22 (CI provides Node 24); uses --network host, so no ports are
# published. The third case intentionally runs a broken-bundle fixture and
# must exit 3; any other outcome fails the target.
## smoke-tizen-engine: boot the TV bundle in pinned Chromium 63 (selenoid/chrome:63.0, Docker required)
smoke-tizen-engine:
	@echo "smoke-tizen-engine: pinned engine selenoid/chrome:63.0 (Google Chrome 63.0.3239.84) — oldest reliably obtainable Chromium at the Tizen 5.0 floor; ceiling of this guarantee"
	node tools/smoke_tizen_engine/smoke.mjs --cases clean,fatal
	@status=0; node tools/smoke_tizen_engine/smoke.mjs --cases broken || status=$$?; \
	if [ "$$status" -eq 3 ]; then \
		echo "smoke-tizen-engine: broken-bundle fixture correctly rejected (case 3, exit 3)"; \
	else \
		echo "smoke-tizen-engine: FAIL — the broken-bundle case must exit 3 (harness detection proven); got $$status" >&2; \
		exit 1; \
	fi
	@echo "smoke-tizen-engine: PASS — clean boot and injected-error panel verified on Google Chrome 63.0.3239.84; broken fixture rejected."

## deploy-pi: build-arm64-headless, then stage binary + systemd units to PI_HOST (make deploy-pi PI_HOST=user@host)
deploy-pi: build-arm64-headless
	PI_HOST="$(PI_HOST)" sh deploy/pi-deploy.sh "$(CURDIR)/bin/filelist-streaming-linux-arm64-headless" "$(CURDIR)/deploy/systemd/filelist-streaming.service" "$(CURDIR)/deploy/systemd/filelist-streaming.logrotate"

## bootstrap-server-dry-run: preview deploy/bootstrap-server.sh server setup without installing anything
bootstrap-server-dry-run:
	@echo "Review only; this target does not install packages."
	sudo sh deploy/bootstrap-server.sh --confirm-server-install --dry-run

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
