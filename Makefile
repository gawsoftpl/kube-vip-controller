.PHONY: tls build cluster k8s-manifest mirrord dev phone

build:
	go build -ldflags="-s -w" -o kube-vip-webhook main.go

cluster:
	scripts/cluster.sh

docker:
	docker build -t kube-vip-controller .

k8s-manifest:
	kubectl apply -f k8s/

mirrord:
	mirrord exec -f ./.mirrord.yaml air

dev:
	air