# Changelog

## [0.5.0](https://github.com/7K-Inari/inari-release-bundle/compare/inari-platform-v0.4.0...inari-platform-v0.5.0) (2026-10-09)


### ⚠ BREAKING CHANGES

* charts/dex is deleted; install Dex via the official dexidp chart (operator-rendered Application or gitops/dev/dex.yaml).
* replace umbrella charts with gitops composition; rewrite CI for ArgoCD e2e
* the Keycloak operator no longer creates an Ingress; enable keycloak.route (or inariServer.route) with gateway.name set to an existing Gateway to expose the services.

### Features

* **chart:** install/upgrade hardening ([#28](https://github.com/7K-Inari/inari-release-bundle/issues/28)) ([3b207fd](https://github.com/7K-Inari/inari-release-bundle/commit/3b207fd17ff8dc6e4474a6debb72233fffe70089))
* **chart:** make console OIDC redirect URIs configurable ([03ede7b](https://github.com/7K-Inari/inari-release-bundle/commit/03ede7b510069877a44d89459513416628d8b310))
* **chart:** make console OIDC redirect URIs configurable ([44a0960](https://github.com/7K-Inari/inari-release-bundle/commit/44a096052a913640ed76917f0ccf73bc8f255458))
* **chart:** skip keycloak role/db creation when using external Keycloak ([26b5db2](https://github.com/7K-Inari/inari-release-bundle/commit/26b5db240d6ea278356ccedcb013286275c9d818))
* **chart:** support shared CNPG/Keycloak operators ([9cc3d23](https://github.com/7K-Inari/inari-release-bundle/commit/9cc3d230ccbc673c3112943178d8a69dbc5b33f6))
* expose routes via Gateway API HTTPRoutes instead of Ingress ([#35](https://github.com/7K-Inari/inari-release-bundle/issues/35)) ([5e93e22](https://github.com/7K-Inari/inari-release-bundle/commit/5e93e22441572d5e6cd1c62ba0aab46d5ae45926))
* external Keycloak + existing-secret support; new inari-console chart ([086c844](https://github.com/7K-Inari/inari-release-bundle/commit/086c8447af21d25020060da72d4f73ed6f08f6e9))
* external Keycloak + existing-secret support; new inari-console chart ([0a2f828](https://github.com/7K-Inari/inari-release-bundle/commit/0a2f828b32467b8a0fe426816e005badae8a5898))
* HTTPRoute annotations and pathPrefix support ([#36](https://github.com/7K-Inari/inari-release-bundle/issues/36)) ([f668a0b](https://github.com/7K-Inari/inari-release-bundle/commit/f668a0b08049a66b75f086f926211c45478f004b))
* **keycloak:** configurable platform admin client role via keycloak.adminRole ([#33](https://github.com/7K-Inari/inari-release-bundle/issues/33)) ([8ec395a](https://github.com/7K-Inari/inari-release-bundle/commit/8ec395aa98763bc1a51c88032c47c984426c6964))
* **keycloak:** service-account admin client instead of admin user/password ([#32](https://github.com/7K-Inari/inari-release-bundle/issues/32)) ([9bc44d3](https://github.com/7K-Inari/inari-release-bundle/commit/9bc44d3ea16d09d61bd0b39295908b9cbfafe5b3))
* **keycloak:** verify admin API access in realm-verify hook job ([#34](https://github.com/7K-Inari/inari-release-bundle/issues/34)) ([3d6e9d9](https://github.com/7K-Inari/inari-release-bundle/commit/3d6e9d9b4b7e6d878db3c91d99c200e95b5b868f))
* **release:** centralize charts in inari-release-bundle + per-merge edge releases ([#54](https://github.com/7K-Inari/inari-release-bundle/issues/54)) ([65b30c1](https://github.com/7K-Inari/inari-release-bundle/commit/65b30c1cced4af9d8fcea202c9ede0f6d4ebbd75))
* replace umbrella charts with gitops composition; rewrite CI for ArgoCD e2e ([1843db0](https://github.com/7K-Inari/inari-release-bundle/commit/1843db0e390e415e61be6f6d09e4b3923017c49b))
* retire in-house dex chart in favor of the official dexidp chart ([#56](https://github.com/7K-Inari/inari-release-bundle/issues/56)) ([850d8ff](https://github.com/7K-Inari/inari-release-bundle/commit/850d8ffe4b7db81be48e9d6711fdd31adb10390e))


### Bug Fixes

* **chart:** reference bootstrap secret keys in postInit role creation ([706d136](https://github.com/7K-Inari/inari-release-bundle/commit/706d136218a50db67bc965b2cb2b61caab9c4151))
* **chart:** reference bootstrap secret keys in postInit role creation ([f269217](https://github.com/7K-Inari/inari-release-bundle/commit/f269217b31d3af1ce47ca529d960fbe1bb4ccf49))
* **inari-platform:** inject KEYCLOAK_CLIENT_SECRET into inari-operator Deployment ([#30](https://github.com/7K-Inari/inari-release-bundle/issues/30)) ([30640ae](https://github.com/7K-Inari/inari-release-bundle/commit/30640ae0e5fba1790acd276b1c5d6e2c59f773bd))

## [0.4.0](https://github.com/7K-Inari/inari-helm-charts/compare/platform-config-v0.3.0...platform-config-v0.4.0) (2026-09-18)


### Features

* **platform-config:** seed platform-admins group with dev-admin member ([#44](https://github.com/7K-Inari/inari-helm-charts/issues/44)) ([e36f4d9](https://github.com/7K-Inari/inari-helm-charts/commit/e36f4d9bab747dd5f79b1fc30b7e35e18fa23694))


### Bug Fixes

* **ci:** bump chart-testing-action to v2.8.0 (stale cosign v2.4.1 pin) ([#48](https://github.com/7K-Inari/inari-helm-charts/issues/48)) ([a937a43](https://github.com/7K-Inari/inari-helm-charts/commit/a937a43db98a8404464ac778262f4cfae8654f5c))

## [0.3.0](https://github.com/7K-Inari/inari-helm-charts/compare/platform-config-v0.2.2...platform-config-v0.3.0) (2026-09-12)


### Features

* **keycloak:** add inari-cli public client (device authorization grant) ([#46](https://github.com/7K-Inari/inari-helm-charts/issues/46)) ([1ebfd66](https://github.com/7K-Inari/inari-helm-charts/commit/1ebfd663c0c4efcb96377aaeead7703bd3e4479c))

## [0.2.2](https://github.com/7K-Inari/inari-helm-charts/compare/platform-config-v0.2.1...platform-config-v0.2.2) (2026-08-30)


### Bug Fixes

* **ci:** consume charts from per-repo GHCR paths and make packages public ([#42](https://github.com/7K-Inari/inari-helm-charts/issues/42)) ([b92543c](https://github.com/7K-Inari/inari-helm-charts/commit/b92543cf552a88869dbdb3de7ea17506c14397ed))

## [0.2.1](https://github.com/7K-Inari/inari-helm-charts/compare/platform-config-v0.2.0...platform-config-v0.2.1) (2026-08-28)


### Bug Fixes

* **release:** align platform-config Chart.yaml to v0.2.0 and use helm release-type ([4426bf6](https://github.com/7K-Inari/inari-helm-charts/commit/4426bf69e5a6b1f7f0bcac6984b951741cf4a07c))

## [0.2.0](https://github.com/7K-Inari/inari-helm-charts/compare/platform-config-v0.1.0...platform-config-v0.2.0) (2026-08-28)


### Features

* add platform-config chart (CNPG cluster + keycloak realm glue) with ArgoCD hooks ([0dc84ca](https://github.com/7K-Inari/inari-helm-charts/commit/0dc84ca63a12199f18fcbd627652cf7db39cd6e6))


### Bug Fixes

* **ci:** yamllint comment spacing in platform-config Chart.yaml ([bddf1cb](https://github.com/7K-Inari/inari-helm-charts/commit/bddf1cbc30135680d3fd510652bc4fa56776f6d4))
* **platform-config:** move client roles to top-level roles.client map ([8315a05](https://github.com/7K-Inari/inari-helm-charts/commit/8315a05a4f270ecb77622a4153ff9c82c6a41ea2))
