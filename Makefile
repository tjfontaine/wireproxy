.PHONY: wireproxy docker

wireproxy:
	go build ./cmd/wireproxy

docker:
	docker build -t wireproxy -f docker/Dockerfile .
	
run:
	docker run \
	--rm --tty --interactive \
	--name=wireproxy \
	--publish 2534:2534 \
	--volume "${PWD}/config:/etc/wireproxy/config:ro" \
	wireproxy