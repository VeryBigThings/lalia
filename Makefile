# Kopos build + install pipeline.
#
# Usage:
#   make build      build the binary into ./bin/kopos
#   make test       go test ./...
#   make install    build + copy to $(PREFIX)/bin/kopos, kick the daemon
#   make uninstall  remove $(PREFIX)/bin/kopos, kick the daemon
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
BIN      := kopos
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

VERSION := $(shell git describe --always --dirty --tags 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test install install-completions uninstall uninstall-completions reload clean help

help:
	@echo "kopos build pipeline"
	@echo ""
	@echo "Targets:"
	@echo "  build                  build $(TARGET) (version=$(VERSION))"
	@echo "  test                   go test ./..."
	@echo "  install                build + install binary and shell completions"
	@echo "  install-completions    install bash/zsh completions only"
	@echo "  uninstall              remove binary + completions"
	@echo "  uninstall-completions  remove completions only"
	@echo "  reload                 kick the running daemon (next call spawns fresh)"
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
	  install -m 0644 completions/_kopos "$(ZSH_COMPDIR)/_kopos"; \
	  echo "installed zsh completion → $(ZSH_COMPDIR)/_kopos"; \
	  rm -f $$HOME/.zcompdump* 2>/dev/null || true; \
	else \
	  echo "skip zsh completion: $(ZSH_COMPDIR) not found"; \
	fi
	@if [ -d "$(BASH_COMPDIR)" ]; then \
	  install -m 0644 completions/kopos.bash "$(BASH_COMPDIR)/kopos"; \
	  echo "installed bash completion → $(BASH_COMPDIR)/kopos"; \
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
	@rm -f "$(ZSH_COMPDIR)/_kopos"  && echo "removed $(ZSH_COMPDIR)/_kopos"  || true
	@rm -f "$(BASH_COMPDIR)/kopos" && echo "removed $(BASH_COMPDIR)/kopos" || true
	@rm -f $$HOME/.zcompdump* 2>/dev/null || true

# Kick the daemon so the next kopos command spawns a fresh one against
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

clean:
	rm -rf $(BUILD_DIR)/
	@echo "cleaned $(BUILD_DIR)/"
