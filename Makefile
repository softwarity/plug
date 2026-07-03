IMAGE   ?= docker.io/softwarity/plug-agent:latest
VERSION ?= dev

.PHONY: cli agent push install clean

cli:
	cd cli && go build -ldflags "-X main.version=$(VERSION)" -o ../bin/plug .

install: cli
	cp bin/plug /opt/homebrew/bin/plug

# Build context is the repo root so the image can compile the CLI binaries it serves.
agent:
	docker build -f agent/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE) .

push: agent
	docker push $(IMAGE)

clean:
	rm -rf bin
