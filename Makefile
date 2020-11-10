IMAGE ?= filefrog/lkebot:latest

default: build

build:
	go build .

docker:
	docker build -t $(IMAGE) .
push: docker
	docker push $(IMAGE)
