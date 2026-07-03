IMAGE ?= ghcr.io/softwarity/plug-agent:latest

.PHONY: cli agent push install clean

cli:
	cd cli && go build -o ../bin/plug .

install: cli
	cp bin/plug /opt/homebrew/bin/plug

agent:
	docker build -t $(IMAGE) agent

push: agent
	docker push $(IMAGE)

clean:
	rm -rf bin
