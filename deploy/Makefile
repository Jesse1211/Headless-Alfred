# Convenience targets that wrap helm + kubectl for the Alfred deploy.
# Run from repo root via:  make -C deploy <target>
#
# These mirror what scripts/deploy-oracle.sh + the GH workflow do, but
# small enough to use ad-hoc from a checkout.

NS      ?= alfred
RELEASE ?= alfred
CHART   := ../deploy/helm/alfred

.PHONY: help lint template upgrade restart logs status rollback teardown

help:
	@echo "Targets:"
	@echo "  lint       helm lint the chart against the oracle values"
	@echo "  template   render the chart (does NOT touch the cluster)"
	@echo "  upgrade    helm upgrade --install (requires ALFRED_USER/PASSWORD/TOKEN env)"
	@echo "  restart    kubectl rollout restart deploy/alfred"
	@echo "  logs       tail pod logs"
	@echo "  status     pod / svc / ingress / pvc summary"
	@echo "  rollback   helm rollback (REV=N override; defaults to previous)"
	@echo "  teardown   uninstall release + drop pvc + drop namespace (DESTRUCTIVE)"

lint:
	helm lint $(CHART) -f $(CHART)/values.yaml -f $(CHART)/values-oracle.yaml

template:
	helm template $(RELEASE) $(CHART) \
		-f $(CHART)/values.yaml -f $(CHART)/values-oracle.yaml \
		--set auth.user=preview --set auth.password=preview --set auth.token=preview

upgrade:
	@bash ../scripts/deploy-oracle.sh

restart:
	kubectl -n $(NS) rollout restart deploy/$(RELEASE)
	kubectl -n $(NS) rollout status deploy/$(RELEASE) --timeout=90s

logs:
	kubectl -n $(NS) logs -f deploy/$(RELEASE)

status:
	@echo "== pods =="
	@kubectl -n $(NS) get pods -o wide
	@echo "== deployment =="
	@kubectl -n $(NS) get deploy/$(RELEASE)
	@echo "== service / ingress / pvc =="
	@kubectl -n $(NS) get svc,ingress,pvc

REV ?=
rollback:
	@if [ -z "$(REV)" ]; then \
		echo "Rolling back to previous revision"; \
		helm -n $(NS) rollback $(RELEASE); \
	else \
		echo "Rolling back to revision $(REV)"; \
		helm -n $(NS) rollback $(RELEASE) $(REV); \
	fi

# DESTRUCTIVE — drops the release AND the PVC. /data is gone after this.
teardown:
	helm -n $(NS) uninstall $(RELEASE) || true
	kubectl -n $(NS) delete pvc alfred-data --ignore-not-found
	kubectl delete namespace $(NS) --ignore-not-found
