IMAGE   ?= docker.io/softwarity/plug:latest
VERSION ?= dev
# Short commit appended as build metadata — matches the Dockerfile/CI, so local
# builds carry a distinct version (dev+<rev>). Without it every local build is
# "dev", and the launcher's check sees "dev == dev" and never updates (no core
# download, so no progress bar). This keeps local builds behaving like prod.
GIT_REV ?= $(shell git rev-parse --short=7 HEAD 2>/dev/null)
V       := $(VERSION)$(if $(GIT_REV),+$(GIT_REV))

.PHONY: cli agent push install clean

cli:
	cd cli && go build -ldflags "-X main.version=$(V)" -o ../bin/plug .

install: cli
	cp bin/plug /opt/homebrew/bin/plug

# Build context is the repo root so the image can compile the CLI binaries it serves.
agent:
	docker build -f agent/Dockerfile --build-arg VERSION=$(VERSION) --build-arg GIT_REV=$(GIT_REV) -t $(IMAGE) .

push: agent
	docker push $(IMAGE)

clean:
	rm -rf bin
