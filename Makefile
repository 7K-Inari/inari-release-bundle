CHART_DIR := charts/inari-platform
CHART_DIRS := $(wildcard charts/*)
GITOPS_DIR := gitops
BACKUP_DIR ?= ./backups

.PHONY: dev-up dev-down backup restore dr-drill lint lint-gitops test package clean

dev-up: ## Create the kind cluster and install the platform stack via gitops/
	./scripts/bootstrap.sh

dev-down: ## Delete the kind dev cluster
	kind delete cluster --name inari-platform

backup: ## Back up PostgreSQL, Keycloak, OpenFGA and NATS into BACKUP_DIR
	./scripts/backup.sh $(BACKUP_DIR)

restore: ## Restore from a backup tarball: make restore BACKUP=backups/inari-backup-<ts>.tgz
	./scripts/restore.sh $(BACKUP)

dr-drill: ## Provision a fresh kind cluster, restore BACKUP and verify: make dr-drill BACKUP=...
	./scripts/dr-drill.sh $(BACKUP)

lint: ## helm lint + chart-testing lint + gitops manifest validation
	helm lint $(CHART_DIR)
	ct lint --config ct.yaml
	$(MAKE) lint-gitops

lint-gitops: ## Validate gitops/ manifests (kustomize builds of the in-repo overlays)
	kubectl kustomize $(GITOPS_DIR)/operators/keycloak-operator > /dev/null
	kubectl kustomize $(GITOPS_DIR)/operators/crossplane-provider-keycloak > /dev/null
	kubectl kustomize $(GITOPS_DIR)/platform/rgd > /dev/null

test: ## helm-unittest suite
	helm unittest $(CHART_DIRS)

validate-graphs: ## Validate inari-platform-graphs against pinned KRO/Crossplane/provider-keycloak CRD schemas
	./scripts/validate-graphs.sh

package: ## Package the chart into dist/
	mkdir -p dist
	helm package $(CHART_DIR) -d dist

clean:
	rm -rf dist $(BACKUP_DIR)
