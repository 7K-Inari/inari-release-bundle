# Changelog

## [0.4.0](https://github.com/7K-Inari/inari-release-bundle/compare/inari-console-v0.3.1...inari-console-v0.4.0) (2026-10-09)


### ⚠ BREAKING CHANGES

* replace umbrella charts with gitops composition; rewrite CI for ArgoCD e2e
* the Keycloak operator no longer creates an Ingress; enable keycloak.route (or inariServer.route) with gateway.name set to an existing Gateway to expose the services.

### Features

* expose routes via Gateway API HTTPRoutes instead of Ingress ([#35](https://github.com/7K-Inari/inari-release-bundle/issues/35)) ([5e93e22](https://github.com/7K-Inari/inari-release-bundle/commit/5e93e22441572d5e6cd1c62ba0aab46d5ae45926))
* external Keycloak + existing-secret support; new inari-console chart ([086c844](https://github.com/7K-Inari/inari-release-bundle/commit/086c8447af21d25020060da72d4f73ed6f08f6e9))
* external Keycloak + existing-secret support; new inari-console chart ([0a2f828](https://github.com/7K-Inari/inari-release-bundle/commit/0a2f828b32467b8a0fe426816e005badae8a5898))
* HTTPRoute annotations and pathPrefix support ([#36](https://github.com/7K-Inari/inari-release-bundle/issues/36)) ([f668a0b](https://github.com/7K-Inari/inari-release-bundle/commit/f668a0b08049a66b75f086f926211c45478f004b))
* **release:** centralize charts in inari-release-bundle + per-merge edge releases ([#54](https://github.com/7K-Inari/inari-release-bundle/issues/54)) ([65b30c1](https://github.com/7K-Inari/inari-release-bundle/commit/65b30c1cced4af9d8fcea202c9ede0f6d4ebbd75))
* replace umbrella charts with gitops composition; rewrite CI for ArgoCD e2e ([1843db0](https://github.com/7K-Inari/inari-release-bundle/commit/1843db0e390e415e61be6f6d09e4b3923017c49b))


### Bug Fixes

* fix listen port for nginx configuration ([53df0dd](https://github.com/7K-Inari/inari-release-bundle/commit/53df0ddd72892c63a48d2297c48de1669825a88e))
* nginx config ([#24](https://github.com/7K-Inari/inari-release-bundle/issues/24)) ([b04d6b9](https://github.com/7K-Inari/inari-release-bundle/commit/b04d6b935a7c5a0eeac6c2cbef41c6fd8b6cd170))
* set cors for auth.7kgroup.org ([ab88e7d](https://github.com/7K-Inari/inari-release-bundle/commit/ab88e7db72a870825b2d8c8e5743e37b719ed3d6))
* templatize nginx configmap ([1db2db2](https://github.com/7K-Inari/inari-release-bundle/commit/1db2db26f72a40e3249b2af44124f57e49052c86))

## [0.3.1](https://github.com/7K-Inari/inari-ui/compare/inari-console-v0.3.0...inari-console-v0.3.1) (2026-09-25)


### Bug Fixes

* **console:** eliminate intermittent white screens (asset caching + root error boundary) ([4c03450](https://github.com/7K-Inari/inari-ui/commit/4c034504c536fd0ef9f0d0bb2140a90d02e5209e))
* **console:** stop white-screening on stale cached index.html after deploys ([101e59c](https://github.com/7K-Inari/inari-ui/commit/101e59cb6cd220dfa533a06d6de8fccc18a99da4))

## Changelog
