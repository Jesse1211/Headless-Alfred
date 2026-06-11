# Operator targets for building, pushing, and applying Headless Alfred.
# Run from repo root via:  make -C deploy <target>
#
# Common overrides:
#   REGISTRY=ghcr.io/<you>          (default: ghcr.io/jesseliu)
#   TAG=v1.0.0                      (default: short git SHA)

REGISTRY ?= ghcr.io/jesseliu
IMAGE    := $(REGISTRY)/headless-alfred
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
FULL     := $(IMAGE):$(TAG)
NS       := alfred

.PHONY: image push apply rollout-restart set-image logs pods status diff teardown

image:
	../scripts/build-image.sh -r $(REGISTRY) -t $(TAG)

push:
	../scripts/build-image.sh -r $(REGISTRY) -t $(TAG) --push

apply:
	kubectl apply -f manifests/namespace.yaml
	@echo "Reminder: 'kubectl -n $(NS) get secret alfred-secret' must exist before the Pod can start."
	@echo "         Create it with the one-liner in deploy/README.md."
	kubectl apply -f manifests/pvc.yaml
	kubectl apply -f manifests/service.yaml
	kubectl apply -f manifests/deployment.yaml
	kubectl apply -f manifests/ingress.yaml

# Bump the running deployment to a new image tag without editing YAML.
# Usage: make -C deploy set-image TAG=v1.2.3
set-image:
	kubectl -n $(NS) set image deployment/alfred alfred=$(FULL)
	kubectl -n $(NS) rollout status deployment/alfred

rollout-restart:
	kubectl -n $(NS) rollout restart deployment/alfred
	kubectl -n $(NS) rollout status deployment/alfred

logs:
	kubectl -n $(NS) logs -f deployment/alfred

pods:
	kubectl -n $(NS) get pods -o wide

status:
	@echo "== deployment =="
	@kubectl -n $(NS) get deployment alfred
	@echo "== pvc =="
	@kubectl -n $(NS) get pvc alfred-data
	@echo "== certificate =="
	@kubectl -n $(NS) get certificate alfred-tls 2>/dev/null || echo "(no certificate yet)"
	@echo "== ingress =="
	@kubectl -n $(NS) get ingress alfred

# Show pending differences without applying.
diff:
	@for f in manifests/*.yaml; do echo "== $$f =="; kubectl diff -f $$f || true; done

# WARNING: removes everything in the alfred namespace. Does NOT touch the
# PVC's PV (storage survives) so history isn't lost.
teardown:
	kubectl delete -f manifests/ingress.yaml --ignore-not-found
	kubectl delete -f manifests/deployment.yaml --ignore-not-found
	kubectl delete -f manifests/service.yaml --ignore-not-found
	kubectl -n $(NS) delete secret alfred-secret --ignore-not-found
	@echo "PVC and Namespace preserved. Delete manually if you really want to drop history."
