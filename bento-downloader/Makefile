IMAGE := quay.io/bentoml/bento-downloader:0.0.4

build:
	docker buildx build --platform linux/amd64 -t ${IMAGE} .
	docker push ${IMAGE}
