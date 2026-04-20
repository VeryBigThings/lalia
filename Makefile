# Lalia build + install pipeline.
#
# Usage:
#   make build      build the binary into ./bin/lalia
#   make test       go test ./...
#   make install    build + copy to $(PREFIX)/bin/lalia, kick the daemon
#   make uninstall  remove $(PREFIX)/bin/lalia, kick the daemon
#   make reload     kick the daemon so next invocation spawns a fresh one
#   make clean      remove ./bin/
#
# PREFIX picks the install root. The default auto-selects the first
# writable directory on $PATH from this list:
#   /opt/homebrew   (Apple Silicon Homebrew, writable without sudo)
#   /usr/local      (Intel/stock, typically needs sudo)
#   $HOME/.local    (user-local fallback)
#
# Override with:  make install PREFIX=/somewhere/else

GO       ?= go
BIN      := lalia
BUILD_DIR:= bin
TARGET   := $(BUILD_DIR)/$(BIN)

# Auto-detect the best PREFIX. User can override via env or make argument.
ifeq ($(origin PREFIX), undefined)
  ifneq ($(wildcard /opt/homebrew/bin/.),)
    ifneq ($(shell test -w /opt/homebrew/bin && echo yes),)
      PREFIX := /opt/homebrew
    endif
  endif
  ifeq ($(origin PREFIX), undefined)
    ifneq ($(wildcard /usr/local/bin/.),)
      ifneq ($(shell test -w /usr/local/bin && echo yes),)
        PREFIX := /usr/local
      endif
    endif
  endif
  ifeq ($(origin PREFIX), undefined)
    PREFIX := $(HOME)/.local
  endif
endif

INSTALL_PATH := $(PREFIX)/bin/$(BIN)

# Shell completion install paths. Only written if the parent dir exists;
# we don't synthesise shell config dirs. Override with ZSH_COMPDIR /
# BASH_COMPDIR env vars. ZSH default matches brew's `site-functions`;
# BASH default matches brew's bash-completion@2 directory.
ZSH_COMPDIR  ?= $(PREFIX)/share/zsh/site-functions
BASH_COMPDIR ?= $(PREFIX)/etc/bash_completion.d

VERSION := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test install install-completions uninstall uninstall-completions reload release check-clean clean help

help:
	@echo "lalia build pipeline"
	@echo ""
	@echo "Targets:"
	@echo "  build                  build $(TARGET) (version=$(VERSION))"
	@echo "  test                   go test ./..."
	@echo "  install                build + install binary and shell completions"
	@echo "  install-completions    install bash/zsh completions only"
	@echo "  uninstall              remove binary + completions"
	@echo "  uninstall-completions  remove completions only"
	@echo "  reload                 kick the running daemon (next call spawns fresh)"
	@echo "  release                git tag the current commit with VERSION"
	@echo "  clean                  remove $(BUILD_DIR)/"
	@echo ""
	@echo "Current PREFIX:      $(PREFIX)"
	@echo "Zsh completions:     $(ZSH_COMPDIR)"
	@echo "Bash completions:    $(BASH_COMPDIR)"

build: $(TARGET)

$(TARGET): *.go go.mod
	@mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(TARGET) .
	@echo "built $(TARGET) (version=$(VERSION))"

test:
	$(GO) test ./...

install: $(TARGET)
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(TARGET) $(INSTALL_PATH)
	@echo "installed $(INSTALL_PATH)"
	@$(MAKE) --no-print-directory install-completions
	@$(MAKE) --no-print-directory reload

# Drop shell completions into their conventional dirs when those dirs
# already exist. Safe to rerun; overwrites the managed file.
install-completions:
	@if [ -d "$(ZSH_COMPDIR)" ]; then \
	  install -m 0644 completions/_lalia "$(ZSH_COMPDIR)/_lalia"; \
	  echo "installed zsh completion → $(ZSH_COMPDIR)/_lalia"; \
	  rm -f $$HOME/.zcompdump* 2>/dev/null || true; \
	else \
	  echo "skip zsh completion: $(ZSH_COMPDIR) not found"; \
	fi
	@if [ -d "$(BASH_COMPDIR)" ]; then \
	  install -m 0644 completions/lalia.bash "$(BASH_COMPDIR)/lalia"; \
	  echo "installed bash completion → $(BASH_COMPDIR)/lalia"; \
	else \
	  echo "skip bash completion: $(BASH_COMPDIR) not found"; \
	fi

uninstall:
	@if [ -f "$(INSTALL_PATH)" ]; then \
	  rm -f $(INSTALL_PATH); \
	  echo "removed $(INSTALL_PATH)"; \
	else \
	  echo "not installed at $(INSTALL_PATH)"; \
	fi
	@$(MAKE) --no-print-directory uninstall-completions
	@$(MAKE) --no-print-directory reload

uninstall-completions:
	@rm -f "$(ZSH_COMPDIR)/_lalia"  && echo "removed $(ZSH_COMPDIR)/_lalia"  || true
	@rm -f "$(BASH_COMPDIR)/lalia" && echo "removed $(BASH_COMPDIR)/lalia" || true
	@rm -f $$HOME/.zcompdump* 2>/dev/null || true

# Kick the daemon so the next lalia command spawns a fresh one against
# the current binary. Registered agents stay registered because the
# registry persists to the workspace git repo; open tunnels die because
# they are in-memory only. A running agent's blocked call returns with
# 'peer_closed' and can simply reopen.
reload:
	@if pgrep -f "$(BIN) --daemon" >/dev/null 2>&1; then \
	  pkill -f "$(BIN) --daemon"; \
	  sleep 0.3; \
	  echo "daemon stopped; next invocation will spawn from new binary"; \
	else \
	  echo "no daemon running"; \
	fi

check-clean:
	@if ! git diff-index --quiet HEAD --; then \
	  echo "error: working directory is dirty; commit your changes first"; \
	  exit 1; \
	fi

release: check-clean
	@if git rev-parse "$(VERSION)" >/dev/null 2>&1; then \
	  echo "error: tag $(VERSION) already exists"; \
	  exit 1; \
	fi
	git tag -a "$(VERSION)" -m "Release $(VERSION)"
	@echo "tagged $(VERSION)"


clean:
	rm -rf $(BUILD_DIR)/
	@echo "cleaned $(BUILD_DIR)/"
