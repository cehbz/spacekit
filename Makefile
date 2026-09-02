# Build, sign, and install the spacekit binaries.
#
# Accessibility/Screen Recording grants are bound to a binary's code
# signature. Ad-hoc signatures change cdhash on every rebuild, which orphans
# the grant and makes macOS re-prompt. Signing with a stable self-signed
# identity (pulled from 1Password at build time, see scripts/sign.sh) keys the
# grant on identifier + certificate instead, so it survives rebuilds.
#
# One-time setup: scripts/gen-signing-cert.sh, store the .p12 in 1Password
# (see README "Stable signing").

CODESIGN_IDENTITY ?= spacekit
PREFIX ?= $(HOME)/bin
BINS := spaceswitch spacekeeper
AGENT := bz.ceh.spacekeeper

# Per-machine signing references (vault/item names) live in a gitignored
# local file so the public Makefile stays generic. See README "Stable signing".
-include signing.local.mk

export CODESIGN_IDENTITY OP_P12_REF OP_P12PW_REF

.PHONY: all build sign install test clean agent-install agent-uninstall

all: install

build:
	go build -o spaceswitch ./cmd/spaceswitch
	go build -o spacekeeper ./cmd/spacekeeper

sign: build
	./scripts/sign.sh $(BINS)

install: sign
	mkdir -p $(PREFIX)
	for b in $(BINS); do cp $$b $(PREFIX)/$$b; done
	@# Restart the daemons if loaded, so the running binaries match what was granted.
	@launchctl kickstart -k gui/$$(id -u)/bz.ceh.spaceswitch 2>/dev/null && echo "restarted spaceswitch daemon" || true
	@launchctl kickstart -k gui/$$(id -u)/$(AGENT) 2>/dev/null && echo "restarted spacekeeper agent" || true
	@echo "installed to $(PREFIX)"

test:
	go test ./...

# The spacekeeper agent (`spacekeeper watch`): periodic and event-driven
# snapshots, restore after a transient display drop, login convergence.
# Replaces the earlier separate save and restore agents, which it unloads.
LEGACY_AGENTS := bz.ceh.spacekeeper-save bz.ceh.spacekeeper-restore
agent-install:
	for a in $(LEGACY_AGENTS); do launchctl bootout gui/$$(id -u)/$$a 2>/dev/null; rm -f $(HOME)/Library/LaunchAgents/$$a.plist; done; true
	cp dist/$(AGENT).plist $(HOME)/Library/LaunchAgents/$(AGENT).plist
	launchctl bootout gui/$$(id -u)/$(AGENT) 2>/dev/null || true
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/$(AGENT).plist
	@echo "spacekeeper agent enabled (log: ~/Library/Logs/spacekeeper/watch.log)"

agent-uninstall:
	launchctl bootout gui/$$(id -u)/$(AGENT) 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/$(AGENT).plist
	@echo "spacekeeper agent disabled"

clean:
	rm -f $(BINS)
