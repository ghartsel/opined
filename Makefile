# OpinEd build helper.
# editor.html is compiled into the binary via go:embed -- ANY change to it
# requires a rebuild to be visible. `make run` always rebuilds first.

PKG_CONFIG_PATH ?= $(HOME)/pkgconfig-shim

HARPER_VERSION ?= 2.4.0
VALE_VERSION ?= 3.12.0
VALE_STYLES ?= Google Microsoft write-good

.PHONY: build run debug clean vendor-harper vendor-vale install

# The binary is fully self-contained (UI + Harper assets embedded), so
# installing is a plain copy. ~/.local/bin is on PATH in most distros.
install: build
	install -D OpinEd $(HOME)/.local/bin/OpinEd
	@echo "Installed to $(HOME)/.local/bin/OpinEd"

build:
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) go build -o OpinEd .

run: build
	./OpinEd $(FILE)

# Run with the WebKit inspector enabled (right-click > Inspect Element)
debug: build
	GFM_DEBUG=1 ./OpinEd $(FILE)

# Download harper.js from the npm registry (curl+tar; no Node needed) and
# place the runtime files under assets/harper (NOT vendor/ -- that name is
# reserved by Go modules). Rebuild afterwards: assets embed via go:embed.
vendor-harper:
	mkdir -p assets/harper
	curl -sL https://registry.npmjs.org/harper.js/-/harper.js-$(HARPER_VERSION).tgz \
	  | tar xz -C assets/harper --strip-components=2 --wildcards 'package/dist/*'
	rm -f assets/harper/*Inlined.js assets/harper/slimBinary.js \
	      assets/harper/harper_wasm_slim_bg.wasm assets/harper/*.d.ts \
	      assets/harper/tsdoc-metadata.json
	@echo "Harper $(HARPER_VERSION) vendored. Now run: make"

# Download the Vale binary and style packages (curl+tar+unzip; no install
# of Vale needed or used) into assets/vale. Rebuild afterwards: assets are
# embedded via go:embed. Styles listed in VALE_STYLES become selectable in
# config.toml ([vale] styles).
vendor-vale:
	mkdir -p assets/vale/styles
	curl -sL https://github.com/errata-ai/vale/releases/download/v$(VALE_VERSION)/vale_$(VALE_VERSION)_Linux_64-bit.tar.gz \
	  | tar xz -C assets/vale vale
	for st in $(VALE_STYLES); do \
	  curl -sLo /tmp/$$st.zip https://github.com/errata-ai/$$st/releases/latest/download/$$st.zip && \
	  unzip -oq /tmp/$$st.zip -d assets/vale/styles && rm -f /tmp/$$st.zip; \
	done
	@echo "Vale $(VALE_VERSION) + styles ($(VALE_STYLES)) vendored. Now run: go mod tidy && make"

clean:
	rm -f OpinEd
