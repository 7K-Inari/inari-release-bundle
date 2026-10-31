# Changelog

## [0.2.0](https://github.com/7K-Inari/inari-release-bundle/compare/inari-platform-graphs-v0.1.0...inari-platform-graphs-v0.2.0) (2026-10-31)


### ⚠ BREAKING CHANGES

* **gitops:** existing hubs must delete the platform.inari.io CRs and CRDs directly (no adoption/migration — D7, pre-release); see the Phase 4 task report for the teardown runbook.

### Features

* close hub gitops gap + tenantnamespace RGD and Keycloak claims ([#103](https://github.com/7K-Inari/inari-release-bundle/issues/103)) ([015d60f](https://github.com/7K-Inari/inari-release-bundle/commit/015d60fe62c29552528b5c14154f3ae246595615))
* **gitops:** remove the inari-operator Applications from the hub (operator sunset Phase 4) ([#116](https://github.com/7K-Inari/inari-release-bundle/issues/116)) ([91d4749](https://github.com/7K-Inari/inari-release-bundle/commit/91d47495bc1e757b2ebae152341993bfc51418aa))
* **keycloak:** migrate realm/client management to provider-keycloak (operator sunset Phase 3) ([#115](https://github.com/7K-Inari/inari-release-bundle/issues/115)) ([baf9a12](https://github.com/7K-Inari/inari-release-bundle/commit/baf9a12288d67b1678bf7586d9e493f242868f40))
