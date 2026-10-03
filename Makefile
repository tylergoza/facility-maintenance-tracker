BINARY := maintenance-tracker
LDFLAGS := -s -w

.PHONY: dev run test build build-linux build-linux-arm docker provision deploy clean

# Serve templates/static from disk: edit HTML/JS/CSS and just refresh.
dev:
	DEV=1 go run . -addr 127.0.0.1:8080

run: build
	./bin/$(BINARY)

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) .

# Digital Ocean droplets / most home-lab servers
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .

# Raspberry Pi 4/5 and other ARM boxes
build-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

docker:
	docker build -t $(BINARY) .

# DigitalOcean droplet via Ansible: see "DigitalOcean with Ansible" in the README.
provision:
	cd deploy/ansible && ansible-playbook provision.yml

deploy:
	cd deploy/ansible && ansible-playbook deploy.yml

clean:
	rm -rf bin
