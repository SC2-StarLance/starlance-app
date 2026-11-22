BINARY      := starlance-app
BINARY_EXE  := starlance-app.exe
BINDIR      := bin
VERSION     ?= dev
LDFLAGS     := -X main.Version=$(VERSION)
WIN_LDFLAGS := -H windowsgui $(LDFLAGS)

# macOS host architecture (arm64 on Apple silicon, amd64 on Intel). A darwin
# binary needs cgo and Apple's frameworks, so it only builds on a mac.
MACOS_ARCH  ?= $(shell go env GOARCH)
APP_NAME    := StarLance App
APP_DIR     := $(BINDIR)/$(APP_NAME).app

.PHONY: all assets build-linux build-windows build-macos app-macos icon sign test clean

all: build-linux build-windows

# The StarCraft II artwork is not stored in this repo (it is Blizzard's). This
# pulls the pinned commit listed in assets.lock, verifying every file's sha256.
assets:
	go run ./tools/fetchassets

build-linux: assets
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags production,webkit2_41 -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./app

build-windows: assets
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags production -ldflags "$(WIN_LDFLAGS)" -o $(BINDIR)/$(BINARY_EXE) ./app

build-macos: assets
	CGO_ENABLED=1 GOOS=darwin GOARCH=$(MACOS_ARCH) go build -tags production -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY)-darwin-$(MACOS_ARCH) ./app

# Double-clickable bundle for macOS: the .app is what Finder opens and what
# carries the Dock/tray icon (Info.plist + AppIcon.icns). Run on a mac, since
# iconutil and the cgo toolchain live there.
app-macos: build-macos
	rm -rf "$(APP_DIR)"
	mkdir -p "$(APP_DIR)/Contents/MacOS" "$(APP_DIR)/Contents/Resources"
	cp "$(BINDIR)/$(BINARY)-darwin-$(MACOS_ARCH)" "$(APP_DIR)/Contents/MacOS/$(BINARY)"
	sh packaging/macos/make-icns.sh app/assets/sl-logo.png "$(APP_DIR)/Contents/Resources/AppIcon.icns"
	sed -e 's/__VERSION__/$(VERSION)/g' packaging/macos/Info.plist > "$(APP_DIR)/Contents/Info.plist"
	@echo "built $(APP_DIR)"

# Windows executable icon: the committed .syso is what release builds link (it
# lives in app/ next to the sources), so
# ImageMagick and mingw are needed only to regenerate it after a logo change.
icon:
	magick app/assets/sl-logo.png -trim +repage -define icon:auto-resize=256,128,64,48,32,24,16 app/assets/win/icon.ico
	x86_64-w64-mingw32-windres -O coff -I app/assets/win -o app/rsrc_windows_amd64.syso app/assets/win/rsrc.rc

# Signs release files for the updater: MINISIGN_SECRET_KEY=... make sign FILES="dist/*"
sign:
	go run ./tools/signrelease $(FILES)

test: assets
	go test ./...

clean:
	rm -rf $(BINDIR)
