# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0/).

## [1.8.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.8.0...v1.8.1) - 2026-07-30

### Features

- **controller:** emit a Warning event on gate/test workflow terminal failure ([#457](https://github.com/shazib-summar/temporal-worker-controller/pull/457)) ([`f7e3c0c`](https://github.com/shazib-summar/temporal-worker-controller/commit/f7e3c0cd28a52d031a8cd9cfe1f6d3b72829843d))
- **helm:** make pod and container securityContext configurable via values ([#404](https://github.com/shazib-summar/temporal-worker-controller/pull/404)) ([`77947df`](https://github.com/shazib-summar/temporal-worker-controller/commit/77947dfa89328b145b343bcdd7791bc2a32e7265))

### Other Changes

- add rbac.createEndUserRoles toggle to skip optional ClusterRoles ([#466](https://github.com/shazib-summar/temporal-worker-controller/pull/466)) ([`251c07d`](https://github.com/shazib-summar/temporal-worker-controller/commit/251c07d8aa6b292f8884af7f2ee59a4cd72639a5))
- Fix/register workerdeployment webhook ([#462](https://github.com/shazib-summar/temporal-worker-controller/pull/462)) ([`f73cf48`](https://github.com/shazib-summar/temporal-worker-controller/commit/f73cf482ee1225130445d6b7acfbc8d0396745c5))
- clean up auth mode and secret name handling ([#452](https://github.com/shazib-summar/temporal-worker-controller/pull/452)) ([`a03a074`](https://github.com/shazib-summar/temporal-worker-controller/commit/a03a074ad12fcb7bf7e19559ad58fdde33a40c47))
- use namespaced Role for manager-role when restrictWatchNamespaces is set ([#450](https://github.com/shazib-summar/temporal-worker-controller/pull/450)) ([`9b0ff0d`](https://github.com/shazib-summar/temporal-worker-controller/commit/9b0ff0d313ea9d0e91c8f38455b7f022fcb62268))
- ensure drained versions stable sort in status ([#458](https://github.com/shazib-summar/temporal-worker-controller/pull/458)) ([`7e13417`](https://github.com/shazib-summar/temporal-worker-controller/commit/7e13417f0f7009f637b0dd09e7c8ae3652729ac5))
- Re-apply rendered WRT resources when a sunset build ID is redeployed; retry failed sunset deletes ([#446](https://github.com/shazib-summar/temporal-worker-controller/pull/446)) ([`557a4e1`](https://github.com/shazib-summar/temporal-worker-controller/commit/557a4e18ed35ef7b94a03e3541d5c1aa7e8f9e0b))
- Bump google.golang.org/grpc from 1.79.3 to 1.82.1 in /internal/demo ([#451](https://github.com/shazib-summar/temporal-worker-controller/pull/451)) ([`4842d79`](https://github.com/shazib-summar/temporal-worker-controller/commit/4842d7950abb876df69202b4619d8eb9a8c94db4))
- Bump docker/setup-buildx-action from 3.0.0 to 4.2.0 ([#443](https://github.com/shazib-summar/temporal-worker-controller/pull/443)) ([`5af2ee5`](https://github.com/shazib-summar/temporal-worker-controller/commit/5af2ee51141c41335de0eb6886110af5ed1d7380))
- Bump golang.org/x/crypto from 0.51.0 to 0.52.0 in /internal/tests ([#436](https://github.com/shazib-summar/temporal-worker-controller/pull/436)) ([`b4c9b94`](https://github.com/shazib-summar/temporal-worker-controller/commit/b4c9b94158afb23caad8297a836fff1011138842))
- Bump imjasonh/setup-crane from 0.4 to 0.7 ([#444](https://github.com/shazib-summar/temporal-worker-controller/pull/444)) ([`0741151`](https://github.com/shazib-summar/temporal-worker-controller/commit/0741151dd72633bc0574a4f23d85ab73f0825deb))
- Bump goreleaser/goreleaser-action from 4.3.0 to 7.2.3 ([#442](https://github.com/shazib-summar/temporal-worker-controller/pull/442)) ([`45804ca`](https://github.com/shazib-summar/temporal-worker-controller/commit/45804ca76001af7ded256038cdf19007369dd757))
- Bump github.com/onsi/ginkgo/v2 in / ([#431](https://github.com/shazib-summar/temporal-worker-controller/pull/431)) ([`b636c7e`](https://github.com/shazib-summar/temporal-worker-controller/commit/b636c7edc85d70725657e86b470222ce532d2aa5))
- Bump docker/login-action from 2.2.0 to 4.3.0 ([#441](https://github.com/shazib-summar/temporal-worker-controller/pull/441)) ([`cc2e805`](https://github.com/shazib-summar/temporal-worker-controller/commit/cc2e805c906f3148c4c5e046418c84a2e74a84d1))
- Bump github.com/onsi/gomega in / ([#432](https://github.com/shazib-summar/temporal-worker-controller/pull/432)) ([`9f2cfc1`](https://github.com/shazib-summar/temporal-worker-controller/commit/9f2cfc13e8c632b349c7c9dbda733512c4e666ff))
- Bump actions/setup-go from 4.0.1 to 6.5.0 ([#430](https://github.com/shazib-summar/temporal-worker-controller/pull/430)) ([`f8278c4`](https://github.com/shazib-summar/temporal-worker-controller/commit/f8278c4aa1525f8750fa1926b0be160064987454))
- Add support to watchNamespace and namespace-scoped rbac for Temporal controller ([#205](https://github.com/shazib-summar/temporal-worker-controller/pull/205)) ([`d398969`](https://github.com/shazib-summar/temporal-worker-controller/commit/d3989695f11db8f46ec9955838be7b1056396a81))
- Bump github.com/onsi/ginkgo/v2 in / ([#417](https://github.com/shazib-summar/temporal-worker-controller/pull/417)) ([`9d40797`](https://github.com/shazib-summar/temporal-worker-controller/commit/9d4079781665132fb62397835cfd8f58c6a799e2))
- Bump golang.org/x/net from 0.53.0 to 0.55.0 in /internal/tests ([#426](https://github.com/shazib-summar/temporal-worker-controller/pull/426)) ([`549dedc`](https://github.com/shazib-summar/temporal-worker-controller/commit/549dedc09a3886591e218dad7051fdd4ff323db1))
- Bump golang.org/x/net from 0.49.0 to 0.55.0 in /internal/demo ([#424](https://github.com/shazib-summar/temporal-worker-controller/pull/424)) ([`5ba3d07`](https://github.com/shazib-summar/temporal-worker-controller/commit/5ba3d0772acb9bec8e4f0e3618cf124ca4dcb865))
- Bump actions/checkout from 6.0.3 to 7.0.0 ([#419](https://github.com/shazib-summar/temporal-worker-controller/pull/419)) ([`43ac5f1`](https://github.com/shazib-summar/temporal-worker-controller/commit/43ac5f1d342ada13f3b70a1f45dadb7fb0742c42))
- Bump golang.org/x/net from 0.53.0 to 0.55.0 ([#425](https://github.com/shazib-summar/temporal-worker-controller/pull/425)) ([`b3d4145`](https://github.com/shazib-summar/temporal-worker-controller/commit/b3d4145b635850b4bd0893a5fec0b50e2119ba24))
- replace Go report card with test status badge ([#423](https://github.com/shazib-summar/temporal-worker-controller/pull/423)) ([`a2c77cd`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2c77cd37dc3a7dcc7b4006c9be7ba9fb1f97f86))
- Bump chart version to 0.27.0 [skip ci] ([`a2e3ccd`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2e3ccdfeadb7a769f74e5cff7ba1cfa4928969d))

## [1.8.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.7.0...v1.8.0) - 2026-06-29

### Features

- **connection:** add tlsServerName for TLS SNI override ([#378](https://github.com/shazib-summar/temporal-worker-controller/pull/378)) ([`94f1ae5`](https://github.com/shazib-summar/temporal-worker-controller/commit/94f1ae5bcb679592c78d4d4a3cf979361e4074f5))
- **wrt:** auto-inject KEDA Temporal trigger metadata for per-version ScaledObjects ([#351](https://github.com/shazib-summar/temporal-worker-controller/pull/351)) ([`74865dc`](https://github.com/shazib-summar/temporal-worker-controller/commit/74865dc858cfc9c1f9653d0bf21ba0d33c69c47b))

### Bug Fixes

- **helm:** use dynamic SA name in proxy-rolebinding subject ([#379](https://github.com/shazib-summar/temporal-worker-controller/pull/379)) ([`564e27d`](https://github.com/shazib-summar/temporal-worker-controller/commit/564e27dd977451ae2f082619d9c2b9dccad5501b))

### Chores

- **deps:** consolidate go mod tidying + one Dependabot PR per dependency ([#376](https://github.com/shazib-summar/temporal-worker-controller/pull/376)) ([`e51f736`](https://github.com/shazib-summar/temporal-worker-controller/commit/e51f736c6d3d0bfe6f95a6cbb5b729909ae6b85f))

### Other Changes

- Add chart values priority and priorityClassName for manager ([#327](https://github.com/shazib-summar/temporal-worker-controller/pull/327)) ([`8f827a5`](https://github.com/shazib-summar/temporal-worker-controller/commit/8f827a5844a979d017cd2dfd970a2f51c03afd23))
- Add docs recommending autoscaling setup ([#324](https://github.com/shazib-summar/temporal-worker-controller/pull/324)) ([`9365d8b`](https://github.com/shazib-summar/temporal-worker-controller/commit/9365d8bcefc7d591eed5d2ccd63986d8423bd3a0))
- VLN-1359: remediate unpinned-github-actions ([#373](https://github.com/shazib-summar/temporal-worker-controller/pull/373)) ([`62d72f8`](https://github.com/shazib-summar/temporal-worker-controller/commit/62d72f8cbb7e55ded8b12fa447f186f5356cf86e))
- use Patch when adding/removing finalizer ([#369](https://github.com/shazib-summar/temporal-worker-controller/pull/369)) ([`409dfce`](https://github.com/shazib-summar/temporal-worker-controller/commit/409dfced7ad568b09c0c03676097adf92c5f170e))
- Scope Deployment cache to worker deployments ([#331](https://github.com/shazib-summar/temporal-worker-controller/pull/331)) ([`c3b1aa2`](https://github.com/shazib-summar/temporal-worker-controller/commit/c3b1aa22b053774543362ebca8978617642801de))
- Bump go.temporal.io/sdk/contrib/opentelemetry from 0.6.0 to 0.7.0 in /internal/demo ([#336](https://github.com/shazib-summar/temporal-worker-controller/pull/336)) ([`8d5187e`](https://github.com/shazib-summar/temporal-worker-controller/commit/8d5187e549bd4504403ee6f6873b81beea7f66b5))
- Bump actions/create-github-app-token from 2 to 3.1.1 ([#335](https://github.com/shazib-summar/temporal-worker-controller/pull/335)) ([`83e72c9`](https://github.com/shazib-summar/temporal-worker-controller/commit/83e72c96fd2b2f7597d402e7e5702eb89661164c))
- Bump actions/checkout from 4 to 6 ([#333](https://github.com/shazib-summar/temporal-worker-controller/pull/333)) ([`24b492e`](https://github.com/shazib-summar/temporal-worker-controller/commit/24b492e0d57810d26946558f528c93f4a0fbceb7))
- VLN-1400: remediate missing-dependency-cooldown ([#325](https://github.com/shazib-summar/temporal-worker-controller/pull/325)) ([`5995992`](https://github.com/shazib-summar/temporal-worker-controller/commit/5995992ac6b826cccc3646d7e66095aa97ad00be))
- Bump github.com/apache/thrift from 0.22.0 to 0.23.0 in /internal/tests ([#319](https://github.com/shazib-summar/temporal-worker-controller/pull/319)) ([`f9d01cd`](https://github.com/shazib-summar/temporal-worker-controller/commit/f9d01cd190674344c285bb9b978f89bcafa5da9f))
- Add imagePullSecrets support for private registry authentication (#322) ([#323](https://github.com/shazib-summar/temporal-worker-controller/pull/323)) ([`d0d1be5`](https://github.com/shazib-summar/temporal-worker-controller/commit/d0d1be5d89327f6e1700f43018cd88fcd0981253))
- upgrade SDK and server to latest for demo Worker UI ([#314](https://github.com/shazib-summar/temporal-worker-controller/pull/314)) ([`29c2ab6`](https://github.com/shazib-summar/temporal-worker-controller/commit/29c2ab6817f28083fed6316713686b8016e9b311))
- Update main README to indicate GA ([#315](https://github.com/shazib-summar/temporal-worker-controller/pull/315)) ([`e4f0bfe`](https://github.com/shazib-summar/temporal-worker-controller/commit/e4f0bfe9b9be1c25c34b1f13690694dcea432c92))
- Bump chart version to 0.26.0 [skip ci] ([`100ead8`](https://github.com/shazib-summar/temporal-worker-controller/commit/100ead8a11f849f6ea344e86e90bc79519bd2dee))
- document downgrade concerns for CRD rename migrate ([#312](https://github.com/shazib-summar/temporal-worker-controller/pull/312)) ([`1ab8573`](https://github.com/shazib-summar/temporal-worker-controller/commit/1ab8573ad2e05de5ca03774af0022c156ffe9b38))
- Fix CEL to actually block deprecated resource create ([#313](https://github.com/shazib-summar/temporal-worker-controller/pull/313)) ([`d89cb15`](https://github.com/shazib-summar/temporal-worker-controller/commit/d89cb156603a439217533edae8d0c3aa00138531))

## [1.7.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.6.0...v1.7.0) - 2026-05-01

### Other Changes

- document downgrade concerns for CRD rename migrate ([#312](https://github.com/shazib-summar/temporal-worker-controller/pull/312)) ([`b1b7257`](https://github.com/shazib-summar/temporal-worker-controller/commit/b1b7257fa13de405320c1cc92c255470ad680966))
- Fix CEL to actually block deprecated resource create ([#313](https://github.com/shazib-summar/temporal-worker-controller/pull/313)) ([`f14adb9`](https://github.com/shazib-summar/temporal-worker-controller/commit/f14adb922383f7f069b5253c27926dbf08914325))
- CRD rename: TemporalWorkerDeployment → WorkerDeployment, TemporalConnection → Connection ([#294](https://github.com/shazib-summar/temporal-worker-controller/pull/294)) ([`bc58d64`](https://github.com/shazib-summar/temporal-worker-controller/commit/bc58d644b7b9418e45644c47f5189eba865d1f3c))
- Bump chart version to 0.25.0 [skip ci] ([`2e8b67a`](https://github.com/shazib-summar/temporal-worker-controller/commit/2e8b67a987b6216f698bdce215bdc85ee442b578))
- Include cluster UID in CONTROLLER_IDENTITY to prevent cross-cluster conflicts ([#309](https://github.com/shazib-summar/temporal-worker-controller/pull/309)) ([`d56306a`](https://github.com/shazib-summar/temporal-worker-controller/commit/d56306aadfedca7f073c92d8cf2067877b1911dd))

## [1.6.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.5.2...v1.6.0) - 2026-04-28

### Bug Fixes

- clean up Temporal server-side versioning data on TWD deletion ([#240](https://github.com/shazib-summar/temporal-worker-controller/pull/240)) ([`b8d9428`](https://github.com/shazib-summar/temporal-worker-controller/commit/b8d9428497ef4580baa65f1d0479427e15c60834))
- accept Opaque secrets for mTLS auth ([#276](https://github.com/shazib-summar/temporal-worker-controller/pull/276)) ([`6c220ba`](https://github.com/shazib-summar/temporal-worker-controller/commit/6c220ba3953788dd3c9fea1ad88a064b091d4efd))

### Other Changes

- Prepare to include cluster UID in CONTROLLER_IDENTITY to prevent cross-cluster conflicts ([#308](https://github.com/shazib-summar/temporal-worker-controller/pull/308)) ([`02e0483`](https://github.com/shazib-summar/temporal-worker-controller/commit/02e0483eede0e09af88cf321b4d2a689ca30aedf))
- remove go.work and binaries from source control ([#305](https://github.com/shazib-summar/temporal-worker-controller/pull/305)) ([`9ff2733`](https://github.com/shazib-summar/temporal-worker-controller/commit/9ff2733463d3c822a21af3abda0e7d0df210aac5))
- Bump github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream from 1.7.4 to 1.7.8 ([#298](https://github.com/shazib-summar/temporal-worker-controller/pull/298)) ([`dc57c5c`](https://github.com/shazib-summar/temporal-worker-controller/commit/dc57c5c3ad9f558d22c5a7a8db107bccf6120ffc))
- Bump github.com/aws/aws-sdk-go-v2/service/lambda from 1.88.0 to 1.88.5 ([#297](https://github.com/shazib-summar/temporal-worker-controller/pull/297)) ([`a0e64ee`](https://github.com/shazib-summar/temporal-worker-controller/commit/a0e64ee8193b35e67f3f53bc5269e042050f8922))
- deprecate authProxy.enabled value option ([#304](https://github.com/shazib-summar/temporal-worker-controller/pull/304)) ([`ecc0f83`](https://github.com/shazib-summar/temporal-worker-controller/commit/ecc0f83521f8453632d6412a9664eb4acbbac26a))
- Bump github.com/go-jose/go-jose/v4 from 4.1.3 to 4.1.4 in /internal/tests ([#258](https://github.com/shazib-summar/temporal-worker-controller/pull/258)) ([`35371a9`](https://github.com/shazib-summar/temporal-worker-controller/commit/35371a9ac950005b3f6912ad5f3dce6933bb11f8))
- Bump github.com/jackc/pgx/v5 from 5.7.2 to 5.9.0 in /internal/tests ([#283](https://github.com/shazib-summar/temporal-worker-controller/pull/283)) ([`24b9105`](https://github.com/shazib-summar/temporal-worker-controller/commit/24b9105ad11b4741ab1c90658db772f1d642da0c))
- Read API key from K8s secret on every RPC call ([#301](https://github.com/shazib-summar/temporal-worker-controller/pull/301)) ([`9e35aad`](https://github.com/shazib-summar/temporal-worker-controller/commit/9e35aad342ced2071cd0763c5204e277a81aa1df))
- Bump go.opentelemetry.io/otel/sdk from 1.40.0 to 1.43.0 ([#266](https://github.com/shazib-summar/temporal-worker-controller/pull/266)) ([`cd5b2d2`](https://github.com/shazib-summar/temporal-worker-controller/commit/cd5b2d2c32b9b5c594452a71fc6ff7dad45abaf6))
- Bump go.opentelemetry.io/otel/sdk from 1.40.0 to 1.43.0 in /internal/demo ([#267](https://github.com/shazib-summar/temporal-worker-controller/pull/267)) ([`4ebff39`](https://github.com/shazib-summar/temporal-worker-controller/commit/4ebff39da76450aa87560f1d6a09913cd1b56e47))
- Bump go.opentelemetry.io/otel/sdk from 1.40.0 to 1.43.0 in /internal/tests ([#273](https://github.com/shazib-summar/temporal-worker-controller/pull/273)) ([`0190f41`](https://github.com/shazib-summar/temporal-worker-controller/commit/0190f418de7d9ad375b66d5d377acea055fa2349))
- Flush SDK client cache on PermissionDenied/Unauthenticated errors ([#300](https://github.com/shazib-summar/temporal-worker-controller/pull/300)) ([`b4913c9`](https://github.com/shazib-summar/temporal-worker-controller/commit/b4913c9eb6cf767df1a0c871f9548cdd124c219f))
- Bump go.opentelemetry.io/otel from 1.40.0 to 1.41.0 ([#299](https://github.com/shazib-summar/temporal-worker-controller/pull/299)) ([`13203fb`](https://github.com/shazib-summar/temporal-worker-controller/commit/13203fbb06f1c203848c3446c57f6a5cb15d93f9))
- release policy and versioning ([#288](https://github.com/shazib-summar/temporal-worker-controller/pull/288)) ([`004cf1d`](https://github.com/shazib-summar/temporal-worker-controller/commit/004cf1d11fe5e9cbf67ff12bb3542fd90b5189bb))
- Back off on `DescribeWorkerDeployment` `ResourceExhausted` error ([#291](https://github.com/shazib-summar/temporal-worker-controller/pull/291)) ([`c09c580`](https://github.com/shazib-summar/temporal-worker-controller/commit/c09c580833ddac3d51013713ec07667eccc4b33d))
- Upgrade dependencies and make PodHash ignore empty fields ([#290](https://github.com/shazib-summar/temporal-worker-controller/pull/290)) ([`1255472`](https://github.com/shazib-summar/temporal-worker-controller/commit/1255472922b35b4ee0df78bb6647ef40a6c139c9))
- Fix events RBAC and automate Helm ClusterRole generation from markers ([#292](https://github.com/shazib-summar/temporal-worker-controller/pull/292)) ([`dab1e34`](https://github.com/shazib-summar/temporal-worker-controller/commit/dab1e344f9f577c0c62efa246d1864f027d5a859))
- Validate TWD spec via CRD CEL rules at apply time (fixes #62) ([#293](https://github.com/shazib-summar/temporal-worker-controller/pull/293)) ([`5f77eb8`](https://github.com/shazib-summar/temporal-worker-controller/commit/5f77eb8c6d85961eba1bef40b22bf3a6391fd1e5))
- Fix formatting in README.md for rollbacks section ([#271](https://github.com/shazib-summar/temporal-worker-controller/pull/271)) ([`3b69480`](https://github.com/shazib-summar/temporal-worker-controller/commit/3b694800bbbf1f3a49a3ba13d4c9112353067043))
- Bump github.com/jackc/pgx/v5 from 5.7.2 to 5.9.2 ([#289](https://github.com/shazib-summar/temporal-worker-controller/pull/289)) ([`f66cc80`](https://github.com/shazib-summar/temporal-worker-controller/commit/f66cc808c6378ef3df54a8cc82c014d214bfdbf0))
- Add helm dependency build step to release workflow ([#279](https://github.com/shazib-summar/temporal-worker-controller/pull/279)) ([`f1e8ea5`](https://github.com/shazib-summar/temporal-worker-controller/commit/f1e8ea5e9e218b9faa584bed177835cd0ed06388))
- Bump chart version to 0.24.1 [skip ci] ([`f8fc3c2`](https://github.com/shazib-summar/temporal-worker-controller/commit/f8fc3c2e79d9a4b3ca470e751c0c5cb72992d84f))

## [1.5.2](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.5.1...v1.5.2) - 2026-04-10

### Other Changes

- Add extra field when doing SubjectAccessReview ([#265](https://github.com/shazib-summar/temporal-worker-controller/pull/265)) ([`51d0812`](https://github.com/shazib-summar/temporal-worker-controller/commit/51d0812eabdb738724ce2b202b93d83a5cb236bd))
- Fix greedy sed in release.yml and restore cert-manager constraint ([#255](https://github.com/shazib-summar/temporal-worker-controller/pull/255)) ([`24f1d21`](https://github.com/shazib-summar/temporal-worker-controller/commit/24f1d2165cf5d83d8ed24afdc5561c0301509e96))
- Bump chart version to 0.24.0 [skip ci] ([`5b50c14`](https://github.com/shazib-summar/temporal-worker-controller/commit/5b50c14792418ea8cbac89e46f35cec6244aa5fb))
- Bump Go to 1.25.8 to fix stdlib CVEs ([#253](https://github.com/shazib-summar/temporal-worker-controller/pull/253)) ([`f679471`](https://github.com/shazib-summar/temporal-worker-controller/commit/f679471ef7e0be80584c363c196672a119fdfb80))
- Skip automatic helm chart bump for patch releases ([#254](https://github.com/shazib-summar/temporal-worker-controller/pull/254)) ([`15b9275`](https://github.com/shazib-summar/temporal-worker-controller/commit/15b9275b123de45e6e2c2df15e8d6c36570813b6))
- Revert cert-manager version constraint to ">=v1.0.0" from 0.23.0 wrongly added by CI ([#252](https://github.com/shazib-summar/temporal-worker-controller/pull/252)) ([`6419f50`](https://github.com/shazib-summar/temporal-worker-controller/commit/6419f50ce7bc5871f7fe4f11aba56a413d9f73e7))
- Bump chart version to 0.23.0 [skip ci] ([`1805045`](https://github.com/shazib-summar/temporal-worker-controller/commit/1805045fe4890e199ee3b2960533ac19a24f8232))
- Fix demo readme and grafana dashboard for autoscaling demo ([#251](https://github.com/shazib-summar/temporal-worker-controller/pull/251)) ([`354b660`](https://github.com/shazib-summar/temporal-worker-controller/commit/354b6602ce79fd4775b2e07e3b77fca1b4556086))

## [1.5.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.5.0...v1.5.1) - 2026-04-01

### Other Changes

- Bump Go to 1.25.8 to fix stdlib CVEs ([#253](https://github.com/shazib-summar/temporal-worker-controller/pull/253)) ([`50822b2`](https://github.com/shazib-summar/temporal-worker-controller/commit/50822b268ee4ec4c59661b00bdbe39048b1e404b))
- Fix demo readme and grafana dashboard for autoscaling demo ([#251](https://github.com/shazib-summar/temporal-worker-controller/pull/251)) ([`35b8e68`](https://github.com/shazib-summar/temporal-worker-controller/commit/35b8e68feedbc3303b61e3fdcc4ce8852099b00a))

## [1.5.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.4.0...v1.5.0) - 2026-03-30

### Other Changes

- Fix demo readme and grafana dashboard for autoscaling demo ([#251](https://github.com/shazib-summar/temporal-worker-controller/pull/251)) ([`354b660`](https://github.com/shazib-summar/temporal-worker-controller/commit/354b6602ce79fd4775b2e07e3b77fca1b4556086))
- Enable Controller-managed versioned scaling resources with `WorkerResourceTemplate` ([#217](https://github.com/shazib-summar/temporal-worker-controller/pull/217)) ([`1123b6b`](https://github.com/shazib-summar/temporal-worker-controller/commit/1123b6bb88e5f0356d5de53cd99b5ee77667f170))
- Bump chart version to 0.22.0 [skip ci] ([`318299a`](https://github.com/shazib-summar/temporal-worker-controller/commit/318299a08c122c5b8f5d4590e72b38a685e7a0af))

## [1.4.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.3.1...v1.4.0) - 2026-03-26

### Features

- replace domain conditions with standard Ready/Progressing conditions ([#235](https://github.com/shazib-summar/temporal-worker-controller/pull/235)) ([`5f00a13`](https://github.com/shazib-summar/temporal-worker-controller/commit/5f00a135b167322b7d0eb3b69fc61c1238e1de4a))

### Bug Fixes

- retry on conflict in test helper to fix flaky integration test ([#244](https://github.com/shazib-summar/temporal-worker-controller/pull/244)) ([`5ab9bf7`](https://github.com/shazib-summar/temporal-worker-controller/commit/5ab9bf7943657b57ecae81a76eb8a35a862cb625))
- lowercase CleanStringForDNS output for RFC 1123 compliance ([#228](https://github.com/shazib-summar/temporal-worker-controller/pull/228)) ([`e2c4fce`](https://github.com/shazib-summar/temporal-worker-controller/commit/e2c4fce951f812f6d35fcec5bf672ed6c0d9a11d))
- append custom CA to system cert pool instead of replacing it ([#227](https://github.com/shazib-summar/temporal-worker-controller/pull/227)) ([`eae7c39`](https://github.com/shazib-summar/temporal-worker-controller/commit/eae7c3996da4006e3f39c3b0d02b9099a5dc847f))

### Documentation

- add Helm ownership labeling step to CRD migration guide ([#245](https://github.com/shazib-summar/temporal-worker-controller/pull/245)) ([`8bc3cdb`](https://github.com/shazib-summar/temporal-worker-controller/commit/8bc3cdbc78f491b6114b270c35e80b06f125215f))

### Other Changes

- Bump chart version to 0.21.0 with appVersion 1.4.0 ([#250](https://github.com/shazib-summar/temporal-worker-controller/pull/250)) ([`303489b`](https://github.com/shazib-summar/temporal-worker-controller/commit/303489b87f6c1a8d2d25ff3cc1672bb3c069e906))
- bump helm chart version to 0.20.0 ([#248](https://github.com/shazib-summar/temporal-worker-controller/pull/248)) ([`97f5520`](https://github.com/shazib-summar/temporal-worker-controller/commit/97f55202781b0ae68409b5253436d7b85e05754f))
- Bump google.golang.org/grpc from 1.75.1 to 1.79.3 ([#243](https://github.com/shazib-summar/temporal-worker-controller/pull/243)) ([`fa0533f`](https://github.com/shazib-summar/temporal-worker-controller/commit/fa0533f06b16dcee81c56e6ab4190e1a15e9828c))
- update the helm.yml workflow to now publish helm charts from feature branches without bumping up the chart version ([#242](https://github.com/shazib-summar/temporal-worker-controller/pull/242)) ([`6908663`](https://github.com/shazib-summar/temporal-worker-controller/commit/690866340be37c03b5fbf045735dea10149f6ef5))
- Add unit tests for clientpool auth code paths ([#236](https://github.com/shazib-summar/temporal-worker-controller/pull/236)) ([`c59e749`](https://github.com/shazib-summar/temporal-worker-controller/commit/c59e7493365af99ac689b97de8f38568c2f2495c))
- Lower reconcile-loop log to debug level ([#238](https://github.com/shazib-summar/temporal-worker-controller/pull/238)) ([`62dbdb8`](https://github.com/shazib-summar/temporal-worker-controller/commit/62dbdb8206d9df6d168b6a0da9c8e011fc83f2e8))
- Bump chart version to 0.19.0 [skip ci] ([`397899a`](https://github.com/shazib-summar/temporal-worker-controller/commit/397899a16d89b69f9d277f000c89cde16ef83f21))
- Bump chart version to 0.18.0 [skip ci] ([`6bd94b0`](https://github.com/shazib-summar/temporal-worker-controller/commit/6bd94b01b12b5903b60f61e3e8824035730e115c))
- Reapply "Use `ManagerIdentity` API instead of `LastModifierIdentity` + ignore-last-modifier metadata hack (#220)" (#233) ([#234](https://github.com/shazib-summar/temporal-worker-controller/pull/234)) ([`7a22c5a`](https://github.com/shazib-summar/temporal-worker-controller/commit/7a22c5a1ab853ba98f9c082ade13e7432559fa4c))
- Bug fix: Do not call CheckHealth when authenticating with API keys ([#232](https://github.com/shazib-summar/temporal-worker-controller/pull/232)) ([`0e04305`](https://github.com/shazib-summar/temporal-worker-controller/commit/0e04305abe23f7ad248fad7dd5602c4cf97c9dcd))
- omit DescribeVersion API calls for drained versions ([#229](https://github.com/shazib-summar/temporal-worker-controller/pull/229)) ([`5c76231`](https://github.com/shazib-summar/temporal-worker-controller/commit/5c762316905ddbf250e1e511bcee718bc0ad4c36))
- Revert "Use `ManagerIdentity` API instead of `LastModifierIdentity` + ignore-last-modifier metadata hack (#220)" ([#233](https://github.com/shazib-summar/temporal-worker-controller/pull/233)) ([`6e8e1d2`](https://github.com/shazib-summar/temporal-worker-controller/commit/6e8e1d20c8393b283065295c8eae9c1b60130af6))
- Use `ManagerIdentity` API instead of `LastModifierIdentity` + ignore-last-modifier metadata hack ([#220](https://github.com/shazib-summar/temporal-worker-controller/pull/220)) ([`fced0ad`](https://github.com/shazib-summar/temporal-worker-controller/commit/fced0adf3fea8efafae3a9f842d887f59e389ff8))
- Separate CRDs Helm chart for upgradeable CRD lifecycle ([#208](https://github.com/shazib-summar/temporal-worker-controller/pull/208)) ([`e9773f5`](https://github.com/shazib-summar/temporal-worker-controller/commit/e9773f56ea734d29688bf37bd746a8a0d40f3490))
- Add manual branch image publish workflow ([#224](https://github.com/shazib-summar/temporal-worker-controller/pull/224)) ([`f394c51`](https://github.com/shazib-summar/temporal-worker-controller/commit/f394c51107e47f0d7b4bf522185a3cadc7df9602))
- Bump chart version to 0.17.0 [skip ci] ([`f43860b`](https://github.com/shazib-summar/temporal-worker-controller/commit/f43860b6cbbe9e4bc8f65d1d265a8624734666f4))
- Bump chart version to 0.16.0 [skip ci] ([`5a0cb13`](https://github.com/shazib-summar/temporal-worker-controller/commit/5a0cb1364e8beecf59bbce2ceeadb1798de67b4e))
- Bump chart version to 0.15.0 [skip ci] ([`60858b3`](https://github.com/shazib-summar/temporal-worker-controller/commit/60858b30d31614b565eafa93289cbe4f01612b7f))

## [1.3.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.3.0...v1.3.1) - 2026-03-20

### Bug Fixes

- append custom CA to system cert pool instead of replacing it ([#227](https://github.com/shazib-summar/temporal-worker-controller/pull/227)) ([`eaf6a8c`](https://github.com/shazib-summar/temporal-worker-controller/commit/eaf6a8cecf6146405f9ace397c20930cad1e2aa0))

### Other Changes

- Bug fix: Do not call CheckHealth when authenticating with API keys ([#232](https://github.com/shazib-summar/temporal-worker-controller/pull/232)) ([`f5d4550`](https://github.com/shazib-summar/temporal-worker-controller/commit/f5d4550a14f89f1b93d799e35c9a0043ff028b64))
- omit DescribeVersion API calls for drained versions ([#229](https://github.com/shazib-summar/temporal-worker-controller/pull/229)) ([`a41c96d`](https://github.com/shazib-summar/temporal-worker-controller/commit/a41c96d46331a5307e3800b04bb0dfd542f3bb40))
- Add manual branch image publish workflow ([#224](https://github.com/shazib-summar/temporal-worker-controller/pull/224)) ([`f10fe19`](https://github.com/shazib-summar/temporal-worker-controller/commit/f10fe198e208da4a0264ac3c23773a63d7de941b))

## [1.3.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.2.4...v1.3.0) - 2026-03-09

### Features

- **api:** add stable build ID override via spec.workerOptions.customBuildID ([#177](https://github.com/shazib-summar/temporal-worker-controller/pull/177)) ([`5784f7a`](https://github.com/shazib-summar/temporal-worker-controller/commit/5784f7ae8efd8bf4129a02b1abb35bc036e9c09d))

### Bug Fixes

- use CA certificate from mTLS secret for server verification ([#212](https://github.com/shazib-summar/temporal-worker-controller/pull/212)) ([`3d0ecf2`](https://github.com/shazib-summar/temporal-worker-controller/commit/3d0ecf29ae2e495e5ade9d23e9613f38693b5030))

### Other Changes

- Bump chart version to 0.14.0 [skip ci] ([`e6aa36c`](https://github.com/shazib-summar/temporal-worker-controller/commit/e6aa36ca13ec09845589f81701893fbe4786a11b))
- Shorten deployment names when over 47 characters ([#204](https://github.com/shazib-summar/temporal-worker-controller/pull/204)) ([`58df597`](https://github.com/shazib-summar/temporal-worker-controller/commit/58df597b814810e2e088b75264e86fdc8dfa8b52))
- Add CI check to verify Helm chart image references are pullable ([#222](https://github.com/shazib-summar/temporal-worker-controller/pull/222)) ([`f79003c`](https://github.com/shazib-summar/temporal-worker-controller/commit/f79003cbb60d4b972ddc412fa3d8a8b9a4a91492))
- Bump go.opentelemetry.io/otel/sdk from 1.34.0 to 1.40.0 ([#213](https://github.com/shazib-summar/temporal-worker-controller/pull/213)) ([`66153f1`](https://github.com/shazib-summar/temporal-worker-controller/commit/66153f150caffeed1935b8a92b2e6fc907683447))
- upgrade server to v1.30.1, API to v1.60.2, SDK to v1.38.0 ([#218](https://github.com/shazib-summar/temporal-worker-controller/pull/218)) ([`9c3ca1d`](https://github.com/shazib-summar/temporal-worker-controller/commit/9c3ca1d49402c11fdb1e177bf4bdc2e6e6fb3b38))
- Bump chart version to 0.13.0 [skip ci] ([`fa275f2`](https://github.com/shazib-summar/temporal-worker-controller/commit/fa275f25da29436995f900319199952110fa8ac5))
- Add event recording and status conditions for worker deployments ([#203](https://github.com/shazib-summar/temporal-worker-controller/pull/203)) ([`872bc38`](https://github.com/shazib-summar/temporal-worker-controller/commit/872bc38c40737d34eb38530af0284be76a349d03))
- Bump filippo.io/edwards25519 from 1.1.0 to 1.1.1 ([#202](https://github.com/shazib-summar/temporal-worker-controller/pull/202)) ([`ceeae89`](https://github.com/shazib-summar/temporal-worker-controller/commit/ceeae893a9d3d24f17831cf87aae32a2120ae615))
- Bump filippo.io/edwards25519 from 1.1.0 to 1.1.1 in /internal/tests ([#201](https://github.com/shazib-summar/temporal-worker-controller/pull/201)) ([`513400c`](https://github.com/shazib-summar/temporal-worker-controller/commit/513400c16f009e0cbed332e2e20656d3b49744c7))
- Bump golang.org/x/crypto from 0.37.0 to 0.45.0 ([#185](https://github.com/shazib-summar/temporal-worker-controller/pull/185)) ([`b2552db`](https://github.com/shazib-summar/temporal-worker-controller/commit/b2552db0bc116ee126e17b0d68099d70b5f4e9d5))
- Helm chart bug Fix: Make sure image has nonRoot ([#195](https://github.com/shazib-summar/temporal-worker-controller/pull/195)) ([`31ccde6`](https://github.com/shazib-summar/temporal-worker-controller/commit/31ccde6d7896efe8b49ff4649003803b166366ee))
- Adding a CI action to validate helm chart renderings ([#199](https://github.com/shazib-summar/temporal-worker-controller/pull/199)) ([`0d0da97`](https://github.com/shazib-summar/temporal-worker-controller/commit/0d0da97dd0031deeb6ae2d4568bd1bb391da98f0))
- Bump chart version to 0.12.0 [skip ci] ([`9e5a646`](https://github.com/shazib-summar/temporal-worker-controller/commit/9e5a646f9b3f00ea332d5b0fe9f23a40b416872b))
- Fix helm chart: Remove templating from values file ([#190](https://github.com/shazib-summar/temporal-worker-controller/pull/190)) ([`03fb38a`](https://github.com/shazib-summar/temporal-worker-controller/commit/03fb38ad1d9f031d8c0ce6b4abe8d66b4ffa332f))
- Helm chart improvements ([#179](https://github.com/shazib-summar/temporal-worker-controller/pull/179)) ([`5e269b8`](https://github.com/shazib-summar/temporal-worker-controller/commit/5e269b824e571bd2db7a154b72a94e7ee51f3146))
- Rename `CustomBuildID` -> `UnsafeCustomBuildID` and integration test it ([#189](https://github.com/shazib-summar/temporal-worker-controller/pull/189)) ([`5d2fa0b`](https://github.com/shazib-summar/temporal-worker-controller/commit/5d2fa0b5c533e0a2642e41b9583517e30234304e))
- Improve docs ([#188](https://github.com/shazib-summar/temporal-worker-controller/pull/188)) ([`8ab2ba5`](https://github.com/shazib-summar/temporal-worker-controller/commit/8ab2ba5f918b6fa6e3ee5844b49fbc07a6607791))
- Bump golang.org/x/crypto from 0.37.0 to 0.45.0 in /internal/tests ([#170](https://github.com/shazib-summar/temporal-worker-controller/pull/170)) ([`9980bfd`](https://github.com/shazib-summar/temporal-worker-controller/commit/9980bfdba541f7853b0b6f217c8a7ed99c9d9aaa))
- Update README.md migration links ([#184](https://github.com/shazib-summar/temporal-worker-controller/pull/184)) ([`3c810bb`](https://github.com/shazib-summar/temporal-worker-controller/commit/3c810bbcc1c83c7ef94ae11a2ee0c89b9fa34ecd))
- Min Server for bug-free Worker Versioning is v1.29.1 ([#175](https://github.com/shazib-summar/temporal-worker-controller/pull/175)) ([`075dff4`](https://github.com/shazib-summar/temporal-worker-controller/commit/075dff4dd8e48b094b3b7284316cb3073e5e30e7))
- Optimize Docker builds: Add .dockerignore, improve caching, and enhance Dockerfile structure ([#164](https://github.com/shazib-summar/temporal-worker-controller/pull/164)) ([`a0eb221`](https://github.com/shazib-summar/temporal-worker-controller/commit/a0eb221665037a67ec6781a8f0f0773d4b32adbd))
- Add docs for how users can go from versioned -> unversioned workers. ([#172](https://github.com/shazib-summar/temporal-worker-controller/pull/172)) ([`ba42a56`](https://github.com/shazib-summar/temporal-worker-controller/commit/ba42a566e6c97ac36ae930c98cae6bbeaf0855be))
- Bump chart version to 0.11.0 [skip ci] ([`297dd18`](https://github.com/shazib-summar/temporal-worker-controller/commit/297dd18663604c3a9107b3dc0475ed64d62a9fdb))

## [1.2.4](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.2.3...v1.2.4) - 2026-03-20

### Bug Fixes

- append custom CA to system cert pool instead of replacing it ([#227](https://github.com/shazib-summar/temporal-worker-controller/pull/227)) ([`fd6f356`](https://github.com/shazib-summar/temporal-worker-controller/commit/fd6f356573c629f97ab01f85eff52249caace7be))

### Other Changes

- buildid to BuildID to comply with the api version ([`ab678cf`](https://github.com/shazib-summar/temporal-worker-controller/commit/ab678cfc5b855e4510008766b0f8738d06d0ac38))
- omit DescribeVersion API calls for drained versions ([#229](https://github.com/shazib-summar/temporal-worker-controller/pull/229)) ([`fb63d39`](https://github.com/shazib-summar/temporal-worker-controller/commit/fb63d397af6cd90d0aa58651e959c40d06ee8be9))
- Add manual branch image publish workflow ([#224](https://github.com/shazib-summar/temporal-worker-controller/pull/224)) ([`c7557f6`](https://github.com/shazib-summar/temporal-worker-controller/commit/c7557f6aa15852bdf3882a4e510ec73ecc9fafae))
- Helm chart bug Fix: Make sure image has nonRoot ([#195](https://github.com/shazib-summar/temporal-worker-controller/pull/195)) ([`94bc3b8`](https://github.com/shazib-summar/temporal-worker-controller/commit/94bc3b8fba6d0cec5f872985cacea4a8d32d542f))

## [1.2.3](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.2.2...v1.2.3) - 2026-03-10

### Bug Fixes

- use CA certificate from mTLS secret for server verification ([#212](https://github.com/shazib-summar/temporal-worker-controller/pull/212)) ([`d2f5387`](https://github.com/shazib-summar/temporal-worker-controller/commit/d2f5387943d8e69a731e0db506a8d3f74f989973))

### Other Changes

- Update image reference for kube-rbac-proxy ([`0ed9072`](https://github.com/shazib-summar/temporal-worker-controller/commit/0ed9072895fef91d1f48d0d1f72bcd0ddd03bf80))

## [1.2.2](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.2.1...v1.2.2) - 2026-03-10

### Other Changes

- Helm chart bug Fix: Make sure image has nonRoot ([#195](https://github.com/shazib-summar/temporal-worker-controller/pull/195)) ([`d10e373`](https://github.com/shazib-summar/temporal-worker-controller/commit/d10e3732702b0881da1073a221364d38fd9ef8b3))

## [1.2.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.2.0...v1.2.1) - 2026-03-06

### Bug Fixes

- use CA certificate from mTLS secret for server verification ([#212](https://github.com/shazib-summar/temporal-worker-controller/pull/212)) ([`db60f92`](https://github.com/shazib-summar/temporal-worker-controller/commit/db60f92fe3350d960e49ae4dad83ff3857421f9c))

## [1.2.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.1.2...v1.2.0) - 2026-01-21

### Features

- **api:** add stable build ID override via spec.workerOptions.customBuildID ([#177](https://github.com/shazib-summar/temporal-worker-controller/pull/177)) ([`5784f7a`](https://github.com/shazib-summar/temporal-worker-controller/commit/5784f7ae8efd8bf4129a02b1abb35bc036e9c09d))

### Other Changes

- Fix helm chart: Remove templating from values file ([#190](https://github.com/shazib-summar/temporal-worker-controller/pull/190)) ([`03fb38a`](https://github.com/shazib-summar/temporal-worker-controller/commit/03fb38ad1d9f031d8c0ce6b4abe8d66b4ffa332f))
- Helm chart improvements ([#179](https://github.com/shazib-summar/temporal-worker-controller/pull/179)) ([`5e269b8`](https://github.com/shazib-summar/temporal-worker-controller/commit/5e269b824e571bd2db7a154b72a94e7ee51f3146))
- Rename `CustomBuildID` -> `UnsafeCustomBuildID` and integration test it ([#189](https://github.com/shazib-summar/temporal-worker-controller/pull/189)) ([`5d2fa0b`](https://github.com/shazib-summar/temporal-worker-controller/commit/5d2fa0b5c533e0a2642e41b9583517e30234304e))
- Improve docs ([#188](https://github.com/shazib-summar/temporal-worker-controller/pull/188)) ([`8ab2ba5`](https://github.com/shazib-summar/temporal-worker-controller/commit/8ab2ba5f918b6fa6e3ee5844b49fbc07a6607791))
- Bump golang.org/x/crypto from 0.37.0 to 0.45.0 in /internal/tests ([#170](https://github.com/shazib-summar/temporal-worker-controller/pull/170)) ([`9980bfd`](https://github.com/shazib-summar/temporal-worker-controller/commit/9980bfdba541f7853b0b6f217c8a7ed99c9d9aaa))
- Update README.md migration links ([#184](https://github.com/shazib-summar/temporal-worker-controller/pull/184)) ([`3c810bb`](https://github.com/shazib-summar/temporal-worker-controller/commit/3c810bbcc1c83c7ef94ae11a2ee0c89b9fa34ecd))
- Min Server for bug-free Worker Versioning is v1.29.1 ([#175](https://github.com/shazib-summar/temporal-worker-controller/pull/175)) ([`075dff4`](https://github.com/shazib-summar/temporal-worker-controller/commit/075dff4dd8e48b094b3b7284316cb3073e5e30e7))
- Optimize Docker builds: Add .dockerignore, improve caching, and enhance Dockerfile structure ([#164](https://github.com/shazib-summar/temporal-worker-controller/pull/164)) ([`a0eb221`](https://github.com/shazib-summar/temporal-worker-controller/commit/a0eb221665037a67ec6781a8f0f0773d4b32adbd))
- Add docs for how users can go from versioned -> unversioned workers. ([#172](https://github.com/shazib-summar/temporal-worker-controller/pull/172)) ([`ba42a56`](https://github.com/shazib-summar/temporal-worker-controller/commit/ba42a566e6c97ac36ae930c98cae6bbeaf0855be))
- Bump chart version to 0.11.0 [skip ci] ([`297dd18`](https://github.com/shazib-summar/temporal-worker-controller/commit/297dd18663604c3a9107b3dc0475ed64d62a9fdb))

## [1.1.2](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.1.1...v1.1.2) - 2026-03-09

### Other Changes

- Update image reference for kube-rbac-proxy ([`0ed9072`](https://github.com/shazib-summar/temporal-worker-controller/commit/0ed9072895fef91d1f48d0d1f72bcd0ddd03bf80))

## [1.1.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.1.0...v1.1.1) - 2025-11-11

### Features

- add support for gating rollouts behind successful workflow executions ([#152](https://github.com/shazib-summar/temporal-worker-controller/pull/152)) ([`fce3c2a`](https://github.com/shazib-summar/temporal-worker-controller/commit/fce3c2a64b879832a041678ffb7f09c0e18e5e5b))

### Other Changes

- Bug Fix: Ignore LastModifierIdentity if server deleted a version for garbage collection ([#163](https://github.com/shazib-summar/temporal-worker-controller/pull/163)) ([`f8c1582`](https://github.com/shazib-summar/temporal-worker-controller/commit/f8c15825bf4cb49cc5755242ff07b642c3b25409))
- VLN-516: Set explicit permissions for GitHub Actions workflows ([#159](https://github.com/shazib-summar/temporal-worker-controller/pull/159)) ([`be53db0`](https://github.com/shazib-summar/temporal-worker-controller/commit/be53db0797091131462a276d978e69ab5fb6646a))
- Ownership docs: Update the docs to reflect the right command. ([#161](https://github.com/shazib-summar/temporal-worker-controller/pull/161)) ([`14322c6`](https://github.com/shazib-summar/temporal-worker-controller/commit/14322c6ad362047e9861139bfb3cd7ea40120a8c))
- Document API key setup and add details about secret creation ([#160](https://github.com/shazib-summar/temporal-worker-controller/pull/160)) ([`dc164ca`](https://github.com/shazib-summar/temporal-worker-controller/commit/dc164cae995407cbce3c829daea4968b9603acb7))
- Refactor integration tests so they can be run one at a time in IDE ([#155](https://github.com/shazib-summar/temporal-worker-controller/pull/155)) ([`d0c1641`](https://github.com/shazib-summar/temporal-worker-controller/commit/d0c16410ff819b8b6dfdf5c7cfcc025f97a15603))
- Bump chart version to 0.10.0 [skip ci] ([`3192bda`](https://github.com/shazib-summar/temporal-worker-controller/commit/3192bdad6065365feb68a8d2434d5e367ba549bc))

## [1.1.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.0.2...v1.1.0) - 2025-10-20

### Other Changes

- update documentation to reflect connectionRef and mutualTLSSecretRef changes in #136 ([#154](https://github.com/shazib-summar/temporal-worker-controller/pull/154)) ([`a2557f9`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2557f9cc22e2e5a69cd6a031d04b397d520d899))
- Use an intermediate environment variable in GHA ([#156](https://github.com/shazib-summar/temporal-worker-controller/pull/156)) ([`7884334`](https://github.com/shazib-summar/temporal-worker-controller/commit/7884334ffe1e0e6361c58fd5edee82930855ef4b))
- Add API key support for the worker-controller ([#149](https://github.com/shazib-summar/temporal-worker-controller/pull/149)) ([`41f5118`](https://github.com/shazib-summar/temporal-worker-controller/commit/41f5118e75567409a9320952b25b7dc1062bf2c7))
- Move helm/temporal-worker-controller/templates/crds to helm/temporal-worker-controller/crds ([#153](https://github.com/shazib-summar/temporal-worker-controller/pull/153)) ([`95221a3`](https://github.com/shazib-summar/temporal-worker-controller/commit/95221a3ea3e3249c9eeb58aff3722edae220def5))
- Bump chart version to 0.9.0 [skip ci] ([`c1bd540`](https://github.com/shazib-summar/temporal-worker-controller/commit/c1bd54028222662e1b39247dcedbac4992e8413b))

## [1.0.2](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.0.1...v1.0.2) - 2025-10-02

### Bug Fixes

- Initial deployment without current version does not get promoted ([#148](https://github.com/shazib-summar/temporal-worker-controller/pull/148)) ([`435a91e`](https://github.com/shazib-summar/temporal-worker-controller/commit/435a91ee970de977d2ffab1ba4bc4690fd0aca12))

### Other Changes

- Remove nonexistent Gate options from docs ([#151](https://github.com/shazib-summar/temporal-worker-controller/pull/151)) ([`a8aaf54`](https://github.com/shazib-summar/temporal-worker-controller/commit/a8aaf54c0597e9833d3ac072d43d03ab51eaa0bc))
- Refactor and add integration tests ([#150](https://github.com/shazib-summar/temporal-worker-controller/pull/150)) ([`474901b`](https://github.com/shazib-summar/temporal-worker-controller/commit/474901bc0f2f8efdf59294a7c93a1128f9b7a1cb))
- Update demo scripts ([#145](https://github.com/shazib-summar/temporal-worker-controller/pull/145)) ([`27ac434`](https://github.com/shazib-summar/temporal-worker-controller/commit/27ac434d428f5ac6597e2e325e01537bf62b5ede))

## [1.0.1](https://github.com/shazib-summar/temporal-worker-controller/compare/v1.0.0...v1.0.1) - 2025-09-24

### Other Changes

- Only Delete Deployments of NotRegistered versions if TemporalState is non-empty ([#147](https://github.com/shazib-summar/temporal-worker-controller/pull/147)) ([`6d80452`](https://github.com/shazib-summar/temporal-worker-controller/commit/6d804523eb784c8e969b35198db4114c0dffebea))
- Bump go.temporal.io/server from 1.28.0 to 1.28.1 in /internal/tests ([#143](https://github.com/shazib-summar/temporal-worker-controller/pull/143)) ([`1340184`](https://github.com/shazib-summar/temporal-worker-controller/commit/13401840197dee85ba2def3cf4a9d89f25f5c635))
- Bump chart version to 0.8.0 [skip ci] ([`f5c8ea7`](https://github.com/shazib-summar/temporal-worker-controller/commit/f5c8ea745d9bd6ef26713cd9fca78068496bf919))

## [1.0.0](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.7...v1.0.0) - 2025-09-11

### Other Changes

- Refactor README for Public Preview ([#137](https://github.com/shazib-summar/temporal-worker-controller/pull/137)) ([`66ca45b`](https://github.com/shazib-summar/temporal-worker-controller/commit/66ca45b00dd5eb38acead351d695106fa63f2316))
- pin artifact onto specific arch ([#141](https://github.com/shazib-summar/temporal-worker-controller/pull/141)) ([`b4068c1`](https://github.com/shazib-summar/temporal-worker-controller/commit/b4068c1371745ab7268b2c0d89d0712e16b93f2e))
- Update integration test ([#139](https://github.com/shazib-summar/temporal-worker-controller/pull/139)) ([`8a4fd39`](https://github.com/shazib-summar/temporal-worker-controller/commit/8a4fd39000951bb804cb808a657b60d62ea10e44))
- Sanitize build IDs to match k8s label value requirements ([#138](https://github.com/shazib-summar/temporal-worker-controller/pull/138)) ([`a2e0207`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2e02070932a8dfc03e66aa5de237169b2ef9305))
- [Breaking change] Follow k8s API conventions ([#136](https://github.com/shazib-summar/temporal-worker-controller/pull/136)) ([`571ae6a`](https://github.com/shazib-summar/temporal-worker-controller/commit/571ae6af8ecbbdfa0a01d8c5e8bd4c2a19ce7dd6))
- Add migration doc draft. ([#91](https://github.com/shazib-summar/temporal-worker-controller/pull/91)) ([`2c388e5`](https://github.com/shazib-summar/temporal-worker-controller/commit/2c388e5f45848416e05c4dd879d510d36d4ca364))
- Fix log spam ([#135](https://github.com/shazib-summar/temporal-worker-controller/pull/135)) ([`751dc8f`](https://github.com/shazib-summar/temporal-worker-controller/commit/751dc8f1bb407c0e42ec961c889048865ec685b3))
- Check versioned and unversioned pollers in certain cases ([#132](https://github.com/shazib-summar/temporal-worker-controller/pull/132)) ([`c4b067a`](https://github.com/shazib-summar/temporal-worker-controller/commit/c4b067ac6cd7f09f4a4b407e5525a0ee9dcd50ee))
- Keep dots in Build IDs and format Worker Deployment names `<k8s-namespace>/<twd-name>` ([#133](https://github.com/shazib-summar/temporal-worker-controller/pull/133)) ([`73e8378`](https://github.com/shazib-summar/temporal-worker-controller/commit/73e83782675ec6d3734748eb8a30d49b231ce458))
- LastModifier-based ownership transfer with docs ([#131](https://github.com/shazib-summar/temporal-worker-controller/pull/131)) ([`51d025a`](https://github.com/shazib-summar/temporal-worker-controller/commit/51d025a5427eca4f6c8a121659afc5f15d092a68))
- [Breaking change] Update TEMPORAL_ injected env vars to match those expected by SDKs ([#130](https://github.com/shazib-summar/temporal-worker-controller/pull/130)) ([`071789e`](https://github.com/shazib-summar/temporal-worker-controller/commit/071789e70726ccd88e95c83eaa77536daad6e0a9))
- Add test cases and arbitrary test case setup function ([#127](https://github.com/shazib-summar/temporal-worker-controller/pull/127)) ([`15aa23b`](https://github.com/shazib-summar/temporal-worker-controller/commit/15aa23bce634dae131e982519930930fe2b74879))
- Update Temporal Go SDK to v1.35.0 and index versions by build ID ([#119](https://github.com/shazib-summar/temporal-worker-controller/pull/119)) ([`6bbcb98`](https://github.com/shazib-summar/temporal-worker-controller/commit/6bbcb98c35afce2a2ed6180f4495547af052133a))
- Fix CI workflow to use go-version-file instead of hardcoded Go version ([#121](https://github.com/shazib-summar/temporal-worker-controller/pull/121)) ([`5572f07`](https://github.com/shazib-summar/temporal-worker-controller/commit/5572f07903b674df0ae94ffc242fb4215662451d))
- Bump chart version to 0.7.0 [skip ci] ([`e39f792`](https://github.com/shazib-summar/temporal-worker-controller/commit/e39f792c9f1ca1a2d3c77a4edd1d1e5f29470f6f))

## [0.1.7](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.6...v0.1.7) - 2025-08-19

### Other Changes

- Update release.yml ([#124](https://github.com/shazib-summar/temporal-worker-controller/pull/124)) ([`d015f94`](https://github.com/shazib-summar/temporal-worker-controller/commit/d015f94ae8eaf1f7aec9719886db3956b098efdc))

## [0.1.6](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.5...v0.1.6) - 2025-08-19

### Other Changes

- Bump chart version to 0.6.0 [skip ci] ([`ce73050`](https://github.com/shazib-summar/temporal-worker-controller/commit/ce73050318e0b7c4de282212628f8e38f51769c3))
- Update release.yml ([#122](https://github.com/shazib-summar/temporal-worker-controller/pull/122)) ([`d7b98eb`](https://github.com/shazib-summar/temporal-worker-controller/commit/d7b98ebc700c30fb256703391933b7ec4aed6339))
- Bump chart version to 0.5.0 [skip ci] ([`28b7c2a`](https://github.com/shazib-summar/temporal-worker-controller/commit/28b7c2a5fc855d139f704b51753b740773793e9e))

## [0.1.5](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.4...v0.1.5) - 2025-08-19

### Other Changes

- Update release.yml to use temporal-cicd[bot] ([#120](https://github.com/shazib-summar/temporal-worker-controller/pull/120)) ([`20aef17`](https://github.com/shazib-summar/temporal-worker-controller/commit/20aef1799869a2dc7c6efd9fe5dfdc755cbe2883))
- TemporalConnection update propagation ([#102](https://github.com/shazib-summar/temporal-worker-controller/pull/102)) ([`5d9fc97`](https://github.com/shazib-summar/temporal-worker-controller/commit/5d9fc97f41a84fc906453b90625d49181221b76f))
- Bump chart version to 0.4.0 [skip ci] ([`40011b7`](https://github.com/shazib-summar/temporal-worker-controller/commit/40011b74778c87448dd18c31d1a5a971f1fe6408))

## [0.1.4](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.3...v0.1.4) - 2025-08-14

### Other Changes

- Use default container command. ([#118](https://github.com/shazib-summar/temporal-worker-controller/pull/118)) ([`aa4bea1`](https://github.com/shazib-summar/temporal-worker-controller/commit/aa4bea150dc895513b67dde159c54964e3c31b9e))
- Fix other helm action's repo paths. ([#117](https://github.com/shazib-summar/temporal-worker-controller/pull/117)) ([`ef4e8ec`](https://github.com/shazib-summar/temporal-worker-controller/commit/ef4e8ec64e599e0358b6db871303f6f69a09c09f))
- Bump chart version to 0.3.0 [skip ci] ([`8573b7a`](https://github.com/shazib-summar/temporal-worker-controller/commit/8573b7a56d606cf8e8c5bb029fd494b4c5274347))

## [0.1.3](https://github.com/shazib-summar/temporal-worker-controller/compare/0.1.2...v0.1.3) - 2025-08-14

### Other Changes

- Adjust to docker.io repo paths. ([#116](https://github.com/shazib-summar/temporal-worker-controller/pull/116)) ([`e185755`](https://github.com/shazib-summar/temporal-worker-controller/commit/e185755306969ed57e7a40e3cc3a2cd0194ebd77))
- Bump chart version to 0.2.0 [skip ci] ([`e0f8ad1`](https://github.com/shazib-summar/temporal-worker-controller/commit/e0f8ad17791f1f54df05438548f72a61ca2a5f62))

## [0.1.2](https://github.com/shazib-summar/temporal-worker-controller/compare/v0.1.2...0.1.2) - 2025-08-13

_No notable changes._

## [0.1.2](https://github.com/shazib-summar/temporal-worker-controller/compare/0.1.1...v0.1.2) - 2025-08-13

### Other Changes

- Change the actions to auth as an app ([#115](https://github.com/shazib-summar/temporal-worker-controller/pull/115)) ([`05a98eb`](https://github.com/shazib-summar/temporal-worker-controller/commit/05a98eb97fc3fc2c598ea5ba8ad86f7581bf4dd1))

## [0.1.1](https://github.com/shazib-summar/temporal-worker-controller/compare/0.1.0...0.1.1) - 2025-08-13

### Other Changes

- Handle v in tag names. ([#114](https://github.com/shazib-summar/temporal-worker-controller/pull/114)) ([`e9817fa`](https://github.com/shazib-summar/temporal-worker-controller/commit/e9817fa0d0be353d82ffb07f4f74e937cfe000e6))

## [0.1.0](https://github.com/shazib-summar/temporal-worker-controller/compare/chart-0.20.0...0.1.0) - 2025-08-13

_No notable changes._

## [chart-0.20.0](https://github.com/shazib-summar/temporal-worker-controller/releases/tag/chart-0.20.0) - 2026-03-24

### Features

- **api:** add stable build ID override via spec.workerOptions.customBuildID ([#177](https://github.com/shazib-summar/temporal-worker-controller/pull/177)) ([`5784f7a`](https://github.com/shazib-summar/temporal-worker-controller/commit/5784f7ae8efd8bf4129a02b1abb35bc036e9c09d))
- add support for gating rollouts behind successful workflow executions ([#152](https://github.com/shazib-summar/temporal-worker-controller/pull/152)) ([`fce3c2a`](https://github.com/shazib-summar/temporal-worker-controller/commit/fce3c2a64b879832a041678ffb7f09c0e18e5e5b))

### Bug Fixes

- append custom CA to system cert pool instead of replacing it ([#227](https://github.com/shazib-summar/temporal-worker-controller/pull/227)) ([`eaf6a8c`](https://github.com/shazib-summar/temporal-worker-controller/commit/eaf6a8cecf6146405f9ace397c20930cad1e2aa0))
- use CA certificate from mTLS secret for server verification ([#212](https://github.com/shazib-summar/temporal-worker-controller/pull/212)) ([`3d0ecf2`](https://github.com/shazib-summar/temporal-worker-controller/commit/3d0ecf29ae2e495e5ade9d23e9613f38693b5030))
- Initial deployment without current version does not get promoted ([#148](https://github.com/shazib-summar/temporal-worker-controller/pull/148)) ([`435a91e`](https://github.com/shazib-summar/temporal-worker-controller/commit/435a91ee970de977d2ffab1ba4bc4690fd0aca12))

### Other Changes

- Bump up the helm chart release version from 0.19 to 0.20 ([#241](https://github.com/shazib-summar/temporal-worker-controller/pull/241)) ([`f59dcd2`](https://github.com/shazib-summar/temporal-worker-controller/commit/f59dcd27ce2593640bdee6b1ed59ee552a84ffbd))
- Bug fix: Do not call CheckHealth when authenticating with API keys ([#232](https://github.com/shazib-summar/temporal-worker-controller/pull/232)) ([`f5d4550`](https://github.com/shazib-summar/temporal-worker-controller/commit/f5d4550a14f89f1b93d799e35c9a0043ff028b64))
- omit DescribeVersion API calls for drained versions ([#229](https://github.com/shazib-summar/temporal-worker-controller/pull/229)) ([`a41c96d`](https://github.com/shazib-summar/temporal-worker-controller/commit/a41c96d46331a5307e3800b04bb0dfd542f3bb40))
- Add manual branch image publish workflow ([#224](https://github.com/shazib-summar/temporal-worker-controller/pull/224)) ([`f10fe19`](https://github.com/shazib-summar/temporal-worker-controller/commit/f10fe198e208da4a0264ac3c23773a63d7de941b))
- Bump chart version to 0.14.0 [skip ci] ([`e6aa36c`](https://github.com/shazib-summar/temporal-worker-controller/commit/e6aa36ca13ec09845589f81701893fbe4786a11b))
- Shorten deployment names when over 47 characters ([#204](https://github.com/shazib-summar/temporal-worker-controller/pull/204)) ([`58df597`](https://github.com/shazib-summar/temporal-worker-controller/commit/58df597b814810e2e088b75264e86fdc8dfa8b52))
- Add CI check to verify Helm chart image references are pullable ([#222](https://github.com/shazib-summar/temporal-worker-controller/pull/222)) ([`f79003c`](https://github.com/shazib-summar/temporal-worker-controller/commit/f79003cbb60d4b972ddc412fa3d8a8b9a4a91492))
- Bump go.opentelemetry.io/otel/sdk from 1.34.0 to 1.40.0 ([#213](https://github.com/shazib-summar/temporal-worker-controller/pull/213)) ([`66153f1`](https://github.com/shazib-summar/temporal-worker-controller/commit/66153f150caffeed1935b8a92b2e6fc907683447))
- upgrade server to v1.30.1, API to v1.60.2, SDK to v1.38.0 ([#218](https://github.com/shazib-summar/temporal-worker-controller/pull/218)) ([`9c3ca1d`](https://github.com/shazib-summar/temporal-worker-controller/commit/9c3ca1d49402c11fdb1e177bf4bdc2e6e6fb3b38))
- Bump chart version to 0.13.0 [skip ci] ([`fa275f2`](https://github.com/shazib-summar/temporal-worker-controller/commit/fa275f25da29436995f900319199952110fa8ac5))
- Add event recording and status conditions for worker deployments ([#203](https://github.com/shazib-summar/temporal-worker-controller/pull/203)) ([`872bc38`](https://github.com/shazib-summar/temporal-worker-controller/commit/872bc38c40737d34eb38530af0284be76a349d03))
- Bump filippo.io/edwards25519 from 1.1.0 to 1.1.1 ([#202](https://github.com/shazib-summar/temporal-worker-controller/pull/202)) ([`ceeae89`](https://github.com/shazib-summar/temporal-worker-controller/commit/ceeae893a9d3d24f17831cf87aae32a2120ae615))
- Bump filippo.io/edwards25519 from 1.1.0 to 1.1.1 in /internal/tests ([#201](https://github.com/shazib-summar/temporal-worker-controller/pull/201)) ([`513400c`](https://github.com/shazib-summar/temporal-worker-controller/commit/513400c16f009e0cbed332e2e20656d3b49744c7))
- Bump golang.org/x/crypto from 0.37.0 to 0.45.0 ([#185](https://github.com/shazib-summar/temporal-worker-controller/pull/185)) ([`b2552db`](https://github.com/shazib-summar/temporal-worker-controller/commit/b2552db0bc116ee126e17b0d68099d70b5f4e9d5))
- Helm chart bug Fix: Make sure image has nonRoot ([#195](https://github.com/shazib-summar/temporal-worker-controller/pull/195)) ([`31ccde6`](https://github.com/shazib-summar/temporal-worker-controller/commit/31ccde6d7896efe8b49ff4649003803b166366ee))
- Adding a CI action to validate helm chart renderings ([#199](https://github.com/shazib-summar/temporal-worker-controller/pull/199)) ([`0d0da97`](https://github.com/shazib-summar/temporal-worker-controller/commit/0d0da97dd0031deeb6ae2d4568bd1bb391da98f0))
- Bump chart version to 0.12.0 [skip ci] ([`9e5a646`](https://github.com/shazib-summar/temporal-worker-controller/commit/9e5a646f9b3f00ea332d5b0fe9f23a40b416872b))
- Fix helm chart: Remove templating from values file ([#190](https://github.com/shazib-summar/temporal-worker-controller/pull/190)) ([`03fb38a`](https://github.com/shazib-summar/temporal-worker-controller/commit/03fb38ad1d9f031d8c0ce6b4abe8d66b4ffa332f))
- Helm chart improvements ([#179](https://github.com/shazib-summar/temporal-worker-controller/pull/179)) ([`5e269b8`](https://github.com/shazib-summar/temporal-worker-controller/commit/5e269b824e571bd2db7a154b72a94e7ee51f3146))
- Rename `CustomBuildID` -> `UnsafeCustomBuildID` and integration test it ([#189](https://github.com/shazib-summar/temporal-worker-controller/pull/189)) ([`5d2fa0b`](https://github.com/shazib-summar/temporal-worker-controller/commit/5d2fa0b5c533e0a2642e41b9583517e30234304e))
- Improve docs ([#188](https://github.com/shazib-summar/temporal-worker-controller/pull/188)) ([`8ab2ba5`](https://github.com/shazib-summar/temporal-worker-controller/commit/8ab2ba5f918b6fa6e3ee5844b49fbc07a6607791))
- Bump golang.org/x/crypto from 0.37.0 to 0.45.0 in /internal/tests ([#170](https://github.com/shazib-summar/temporal-worker-controller/pull/170)) ([`9980bfd`](https://github.com/shazib-summar/temporal-worker-controller/commit/9980bfdba541f7853b0b6f217c8a7ed99c9d9aaa))
- Update README.md migration links ([#184](https://github.com/shazib-summar/temporal-worker-controller/pull/184)) ([`3c810bb`](https://github.com/shazib-summar/temporal-worker-controller/commit/3c810bbcc1c83c7ef94ae11a2ee0c89b9fa34ecd))
- Min Server for bug-free Worker Versioning is v1.29.1 ([#175](https://github.com/shazib-summar/temporal-worker-controller/pull/175)) ([`075dff4`](https://github.com/shazib-summar/temporal-worker-controller/commit/075dff4dd8e48b094b3b7284316cb3073e5e30e7))
- Optimize Docker builds: Add .dockerignore, improve caching, and enhance Dockerfile structure ([#164](https://github.com/shazib-summar/temporal-worker-controller/pull/164)) ([`a0eb221`](https://github.com/shazib-summar/temporal-worker-controller/commit/a0eb221665037a67ec6781a8f0f0773d4b32adbd))
- Add docs for how users can go from versioned -> unversioned workers. ([#172](https://github.com/shazib-summar/temporal-worker-controller/pull/172)) ([`ba42a56`](https://github.com/shazib-summar/temporal-worker-controller/commit/ba42a566e6c97ac36ae930c98cae6bbeaf0855be))
- Bump chart version to 0.11.0 [skip ci] ([`297dd18`](https://github.com/shazib-summar/temporal-worker-controller/commit/297dd18663604c3a9107b3dc0475ed64d62a9fdb))
- Bug Fix: Ignore LastModifierIdentity if server deleted a version for garbage collection ([#163](https://github.com/shazib-summar/temporal-worker-controller/pull/163)) ([`f8c1582`](https://github.com/shazib-summar/temporal-worker-controller/commit/f8c15825bf4cb49cc5755242ff07b642c3b25409))
- VLN-516: Set explicit permissions for GitHub Actions workflows ([#159](https://github.com/shazib-summar/temporal-worker-controller/pull/159)) ([`be53db0`](https://github.com/shazib-summar/temporal-worker-controller/commit/be53db0797091131462a276d978e69ab5fb6646a))
- Ownership docs: Update the docs to reflect the right command. ([#161](https://github.com/shazib-summar/temporal-worker-controller/pull/161)) ([`14322c6`](https://github.com/shazib-summar/temporal-worker-controller/commit/14322c6ad362047e9861139bfb3cd7ea40120a8c))
- Document API key setup and add details about secret creation ([#160](https://github.com/shazib-summar/temporal-worker-controller/pull/160)) ([`dc164ca`](https://github.com/shazib-summar/temporal-worker-controller/commit/dc164cae995407cbce3c829daea4968b9603acb7))
- Refactor integration tests so they can be run one at a time in IDE ([#155](https://github.com/shazib-summar/temporal-worker-controller/pull/155)) ([`d0c1641`](https://github.com/shazib-summar/temporal-worker-controller/commit/d0c16410ff819b8b6dfdf5c7cfcc025f97a15603))
- Bump chart version to 0.10.0 [skip ci] ([`3192bda`](https://github.com/shazib-summar/temporal-worker-controller/commit/3192bdad6065365feb68a8d2434d5e367ba549bc))
- update documentation to reflect connectionRef and mutualTLSSecretRef changes in #136 ([#154](https://github.com/shazib-summar/temporal-worker-controller/pull/154)) ([`a2557f9`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2557f9cc22e2e5a69cd6a031d04b397d520d899))
- Use an intermediate environment variable in GHA ([#156](https://github.com/shazib-summar/temporal-worker-controller/pull/156)) ([`7884334`](https://github.com/shazib-summar/temporal-worker-controller/commit/7884334ffe1e0e6361c58fd5edee82930855ef4b))
- Add API key support for the worker-controller ([#149](https://github.com/shazib-summar/temporal-worker-controller/pull/149)) ([`41f5118`](https://github.com/shazib-summar/temporal-worker-controller/commit/41f5118e75567409a9320952b25b7dc1062bf2c7))
- Move helm/temporal-worker-controller/templates/crds to helm/temporal-worker-controller/crds ([#153](https://github.com/shazib-summar/temporal-worker-controller/pull/153)) ([`95221a3`](https://github.com/shazib-summar/temporal-worker-controller/commit/95221a3ea3e3249c9eeb58aff3722edae220def5))
- Bump chart version to 0.9.0 [skip ci] ([`c1bd540`](https://github.com/shazib-summar/temporal-worker-controller/commit/c1bd54028222662e1b39247dcedbac4992e8413b))
- Remove nonexistent Gate options from docs ([#151](https://github.com/shazib-summar/temporal-worker-controller/pull/151)) ([`a8aaf54`](https://github.com/shazib-summar/temporal-worker-controller/commit/a8aaf54c0597e9833d3ac072d43d03ab51eaa0bc))
- Refactor and add integration tests ([#150](https://github.com/shazib-summar/temporal-worker-controller/pull/150)) ([`474901b`](https://github.com/shazib-summar/temporal-worker-controller/commit/474901bc0f2f8efdf59294a7c93a1128f9b7a1cb))
- Update demo scripts ([#145](https://github.com/shazib-summar/temporal-worker-controller/pull/145)) ([`27ac434`](https://github.com/shazib-summar/temporal-worker-controller/commit/27ac434d428f5ac6597e2e325e01537bf62b5ede))
- Only Delete Deployments of NotRegistered versions if TemporalState is non-empty ([#147](https://github.com/shazib-summar/temporal-worker-controller/pull/147)) ([`6d80452`](https://github.com/shazib-summar/temporal-worker-controller/commit/6d804523eb784c8e969b35198db4114c0dffebea))
- Bump go.temporal.io/server from 1.28.0 to 1.28.1 in /internal/tests ([#143](https://github.com/shazib-summar/temporal-worker-controller/pull/143)) ([`1340184`](https://github.com/shazib-summar/temporal-worker-controller/commit/13401840197dee85ba2def3cf4a9d89f25f5c635))
- Bump chart version to 0.8.0 [skip ci] ([`f5c8ea7`](https://github.com/shazib-summar/temporal-worker-controller/commit/f5c8ea745d9bd6ef26713cd9fca78068496bf919))
- Refactor README for Public Preview ([#137](https://github.com/shazib-summar/temporal-worker-controller/pull/137)) ([`66ca45b`](https://github.com/shazib-summar/temporal-worker-controller/commit/66ca45b00dd5eb38acead351d695106fa63f2316))
- pin artifact onto specific arch ([#141](https://github.com/shazib-summar/temporal-worker-controller/pull/141)) ([`b4068c1`](https://github.com/shazib-summar/temporal-worker-controller/commit/b4068c1371745ab7268b2c0d89d0712e16b93f2e))
- Update integration test ([#139](https://github.com/shazib-summar/temporal-worker-controller/pull/139)) ([`8a4fd39`](https://github.com/shazib-summar/temporal-worker-controller/commit/8a4fd39000951bb804cb808a657b60d62ea10e44))
- Sanitize build IDs to match k8s label value requirements ([#138](https://github.com/shazib-summar/temporal-worker-controller/pull/138)) ([`a2e0207`](https://github.com/shazib-summar/temporal-worker-controller/commit/a2e02070932a8dfc03e66aa5de237169b2ef9305))
- [Breaking change] Follow k8s API conventions ([#136](https://github.com/shazib-summar/temporal-worker-controller/pull/136)) ([`571ae6a`](https://github.com/shazib-summar/temporal-worker-controller/commit/571ae6af8ecbbdfa0a01d8c5e8bd4c2a19ce7dd6))
- Add migration doc draft. ([#91](https://github.com/shazib-summar/temporal-worker-controller/pull/91)) ([`2c388e5`](https://github.com/shazib-summar/temporal-worker-controller/commit/2c388e5f45848416e05c4dd879d510d36d4ca364))
- Fix log spam ([#135](https://github.com/shazib-summar/temporal-worker-controller/pull/135)) ([`751dc8f`](https://github.com/shazib-summar/temporal-worker-controller/commit/751dc8f1bb407c0e42ec961c889048865ec685b3))
- Check versioned and unversioned pollers in certain cases ([#132](https://github.com/shazib-summar/temporal-worker-controller/pull/132)) ([`c4b067a`](https://github.com/shazib-summar/temporal-worker-controller/commit/c4b067ac6cd7f09f4a4b407e5525a0ee9dcd50ee))
- Keep dots in Build IDs and format Worker Deployment names `<k8s-namespace>/<twd-name>` ([#133](https://github.com/shazib-summar/temporal-worker-controller/pull/133)) ([`73e8378`](https://github.com/shazib-summar/temporal-worker-controller/commit/73e83782675ec6d3734748eb8a30d49b231ce458))
- LastModifier-based ownership transfer with docs ([#131](https://github.com/shazib-summar/temporal-worker-controller/pull/131)) ([`51d025a`](https://github.com/shazib-summar/temporal-worker-controller/commit/51d025a5427eca4f6c8a121659afc5f15d092a68))
- [Breaking change] Update TEMPORAL_ injected env vars to match those expected by SDKs ([#130](https://github.com/shazib-summar/temporal-worker-controller/pull/130)) ([`071789e`](https://github.com/shazib-summar/temporal-worker-controller/commit/071789e70726ccd88e95c83eaa77536daad6e0a9))
- Add test cases and arbitrary test case setup function ([#127](https://github.com/shazib-summar/temporal-worker-controller/pull/127)) ([`15aa23b`](https://github.com/shazib-summar/temporal-worker-controller/commit/15aa23bce634dae131e982519930930fe2b74879))
- Update Temporal Go SDK to v1.35.0 and index versions by build ID ([#119](https://github.com/shazib-summar/temporal-worker-controller/pull/119)) ([`6bbcb98`](https://github.com/shazib-summar/temporal-worker-controller/commit/6bbcb98c35afce2a2ed6180f4495547af052133a))
- Fix CI workflow to use go-version-file instead of hardcoded Go version ([#121](https://github.com/shazib-summar/temporal-worker-controller/pull/121)) ([`5572f07`](https://github.com/shazib-summar/temporal-worker-controller/commit/5572f07903b674df0ae94ffc242fb4215662451d))
- Bump chart version to 0.7.0 [skip ci] ([`e39f792`](https://github.com/shazib-summar/temporal-worker-controller/commit/e39f792c9f1ca1a2d3c77a4edd1d1e5f29470f6f))
- Update release.yml ([#124](https://github.com/shazib-summar/temporal-worker-controller/pull/124)) ([`d015f94`](https://github.com/shazib-summar/temporal-worker-controller/commit/d015f94ae8eaf1f7aec9719886db3956b098efdc))
- Bump chart version to 0.6.0 [skip ci] ([`ce73050`](https://github.com/shazib-summar/temporal-worker-controller/commit/ce73050318e0b7c4de282212628f8e38f51769c3))
- Update release.yml ([#122](https://github.com/shazib-summar/temporal-worker-controller/pull/122)) ([`d7b98eb`](https://github.com/shazib-summar/temporal-worker-controller/commit/d7b98ebc700c30fb256703391933b7ec4aed6339))
- Bump chart version to 0.5.0 [skip ci] ([`28b7c2a`](https://github.com/shazib-summar/temporal-worker-controller/commit/28b7c2a5fc855d139f704b51753b740773793e9e))
- Update release.yml to use temporal-cicd[bot] ([#120](https://github.com/shazib-summar/temporal-worker-controller/pull/120)) ([`20aef17`](https://github.com/shazib-summar/temporal-worker-controller/commit/20aef1799869a2dc7c6efd9fe5dfdc755cbe2883))
- TemporalConnection update propagation ([#102](https://github.com/shazib-summar/temporal-worker-controller/pull/102)) ([`5d9fc97`](https://github.com/shazib-summar/temporal-worker-controller/commit/5d9fc97f41a84fc906453b90625d49181221b76f))
- Bump chart version to 0.4.0 [skip ci] ([`40011b7`](https://github.com/shazib-summar/temporal-worker-controller/commit/40011b74778c87448dd18c31d1a5a971f1fe6408))
- Use default container command. ([#118](https://github.com/shazib-summar/temporal-worker-controller/pull/118)) ([`aa4bea1`](https://github.com/shazib-summar/temporal-worker-controller/commit/aa4bea150dc895513b67dde159c54964e3c31b9e))
- Fix other helm action's repo paths. ([#117](https://github.com/shazib-summar/temporal-worker-controller/pull/117)) ([`ef4e8ec`](https://github.com/shazib-summar/temporal-worker-controller/commit/ef4e8ec64e599e0358b6db871303f6f69a09c09f))
- Bump chart version to 0.3.0 [skip ci] ([`8573b7a`](https://github.com/shazib-summar/temporal-worker-controller/commit/8573b7a56d606cf8e8c5bb029fd494b4c5274347))
- Adjust to docker.io repo paths. ([#116](https://github.com/shazib-summar/temporal-worker-controller/pull/116)) ([`e185755`](https://github.com/shazib-summar/temporal-worker-controller/commit/e185755306969ed57e7a40e3cc3a2cd0194ebd77))
- Bump chart version to 0.2.0 [skip ci] ([`e0f8ad1`](https://github.com/shazib-summar/temporal-worker-controller/commit/e0f8ad17791f1f54df05438548f72a61ca2a5f62))
- Change the actions to auth as an app ([#115](https://github.com/shazib-summar/temporal-worker-controller/pull/115)) ([`05a98eb`](https://github.com/shazib-summar/temporal-worker-controller/commit/05a98eb97fc3fc2c598ea5ba8ad86f7581bf4dd1))
- Handle v in tag names. ([#114](https://github.com/shazib-summar/temporal-worker-controller/pull/114)) ([`e9817fa`](https://github.com/shazib-summar/temporal-worker-controller/commit/e9817fa0d0be353d82ffb07f4f74e937cfe000e6))
- Add helm to release process. ([#111](https://github.com/shazib-summar/temporal-worker-controller/pull/111)) ([`4c468d4`](https://github.com/shazib-summar/temporal-worker-controller/commit/4c468d4b9505118f3c68126deb0565960980c7be))
- Switch to dockerhub temporalio org for images. ([#110](https://github.com/shazib-summar/temporal-worker-controller/pull/110)) ([`f819246`](https://github.com/shazib-summar/temporal-worker-controller/commit/f81924689efeae6d5d1cbcec1fa417c57aab9a0c))
- Release artifacts ([#107](https://github.com/shazib-summar/temporal-worker-controller/pull/107)) ([`df5d4e8`](https://github.com/shazib-summar/temporal-worker-controller/commit/df5d4e80f3ed9d26a5917f3453825fe9f71876c6))
- Add .claude to gitignore ([#105](https://github.com/shazib-summar/temporal-worker-controller/pull/105)) ([`12bbc60`](https://github.com/shazib-summar/temporal-worker-controller/commit/12bbc60cbd8828e4ddb1f42b77446b3e047d0c96))
- add links to README and update terminology ([#106](https://github.com/shazib-summar/temporal-worker-controller/pull/106)) ([`7600df0`](https://github.com/shazib-summar/temporal-worker-controller/commit/7600df0e49ce253461cc700f8c71b832a8f63e39))
- Remove infinite version existence check ([#98](https://github.com/shazib-summar/temporal-worker-controller/pull/98)) ([`5b2ad52`](https://github.com/shazib-summar/temporal-worker-controller/commit/5b2ad5246249c1c4c69f0aefd22aeb725c7e1802))
- fix docker-build make target ([#100](https://github.com/shazib-summar/temporal-worker-controller/pull/100)) ([`fff3e8f`](https://github.com/shazib-summar/temporal-worker-controller/commit/fff3e8f2a178e902f1e7ebdee296b48ce4130ceb))
- Document nil current version rollout behavior ([#99](https://github.com/shazib-summar/temporal-worker-controller/pull/99)) ([`ad056be`](https://github.com/shazib-summar/temporal-worker-controller/commit/ad056be79d1c7317106641ef60e58f2e4798b50f))
- Integration test framework ([#78](https://github.com/shazib-summar/temporal-worker-controller/pull/78)) ([`1c5f057`](https://github.com/shazib-summar/temporal-worker-controller/commit/1c5f0576b1e51145e52359a110ea90cb748a69d3))
- Update local README + demo ([#94](https://github.com/shazib-summar/temporal-worker-controller/pull/94)) ([`4e67acc`](https://github.com/shazib-summar/temporal-worker-controller/commit/4e67acc63f4f8e6436617441057696f6a526f4ae))
- Use `rollout` rather than `cutover` in CRD JSON. ([#93](https://github.com/shazib-summar/temporal-worker-controller/pull/93)) ([`fc3f632`](https://github.com/shazib-summar/temporal-worker-controller/commit/fc3f6320f88d1383c70ad2589ba428eedf5f992c))
- added a TODO about the bug ([`1050d30`](https://github.com/shazib-summar/temporal-worker-controller/commit/1050d30013ceaf24a6e213d2f0f036682d4912f0))
- feedback ([`fbca852`](https://github.com/shazib-summar/temporal-worker-controller/commit/fbca8523fa4f1fa99946ed5071d19cc12a22bd57))
- better naming for helper function ([`41135a0`](https://github.com/shazib-summar/temporal-worker-controller/commit/41135a09e464ab0506ec3451f20d055c8093de5c))
- cleanup pt 2 ([`9268011`](https://github.com/shazib-summar/temporal-worker-controller/commit/926801184594fcb91e6b48d885994ee92f402926))
- cleanup ([`28f8486`](https://github.com/shazib-summar/temporal-worker-controller/commit/28f8486ef2eca0048f36c4124c1c53d31a453a40))
- Merge pull request #84 from robholland/rh-deployment-name ([`3e1a0e0`](https://github.com/shazib-summar/temporal-worker-controller/commit/3e1a0e00436d1e059a9ea001f88a2b226cc0789f))
- remove some prints ([`c2b781d`](https://github.com/shazib-summar/temporal-worker-controller/commit/c2b781ddeba582da9a9e6241048804085eaf43b6))
- general bug fixes ([`74e5b42`](https://github.com/shazib-summar/temporal-worker-controller/commit/74e5b427f59f464bf3113412e30bd5c8b80009a4))
- Bump golang.org/x/net from 0.29.0 to 0.38.0 ([`ca7dcb6`](https://github.com/shazib-summar/temporal-worker-controller/commit/ca7dcb602d87de250ac9dc31f0ac2d4179fd94a7))
- Bump golang.org/x/oauth2 from 0.22.0 to 0.27.0 ([`8321fca`](https://github.com/shazib-summar/temporal-worker-controller/commit/8321fca33773d5b722f4dd8be4b9998b4426881e))
- Adjust API so TargetVersion is required. ([`6375ce0`](https://github.com/shazib-summar/temporal-worker-controller/commit/6375ce0fdc7fa2842645a992596a7996956b0dd6))
- Shift defaults to an internal package. ([`4938ba6`](https://github.com/shazib-summar/temporal-worker-controller/commit/4938ba653840d31d8dd6c7d5da9b5aca4e2dfd03))
- Rely on kubebuilder validation for MaxVersions. ([`d5ea09a`](https://github.com/shazib-summar/temporal-worker-controller/commit/d5ea09a7321bb98738a132ded25368bae79b86c4))
- Adjust comment. ([`23ae61a`](https://github.com/shazib-summar/temporal-worker-controller/commit/23ae61a8e270a87195f8db32c0935aa2f03249e2))
- Make TargetVersion required. ([`c223c17`](https://github.com/shazib-summar/temporal-worker-controller/commit/c223c17a5a66f590c1138aca68d600cf58ecf225))
- Avoid magic numbers. ([`1f959df`](https://github.com/shazib-summar/temporal-worker-controller/commit/1f959dff67817738d4618359595f76013428578e))
- Log when we aren't able to deploy due to max versions limit. ([`3e4eb26`](https://github.com/shazib-summar/temporal-worker-controller/commit/3e4eb26a04d328641a4b7f221c786ba81e674419))
- Bring validation down to current server side limit for max versions. ([`9ad1245`](https://github.com/shazib-summar/temporal-worker-controller/commit/9ad12456e118547c69aae0607d5e3f73fd61dfb0))
- Update api/v1alpha1/worker_types.go ([`7b78331`](https://github.com/shazib-summar/temporal-worker-controller/commit/7b7833139676575a7d9eb91bcf417005d0d1c3db))
- Add a version limit. ([`6fdbeb3`](https://github.com/shazib-summar/temporal-worker-controller/commit/6fdbeb39142a0cd7d0aa4067b4a9dab87e66bfba))
- Remove some nil guards which should not be needed. ([`39cc535`](https://github.com/shazib-summar/temporal-worker-controller/commit/39cc5354cc91a57a513d5b3c1c6efb7f94378851))
- WIP. ([`37a4005`](https://github.com/shazib-summar/temporal-worker-controller/commit/37a40057b7146785e7569c7f03cc819b3b1a5565))
- Wait for at least last step duration ([#79](https://github.com/shazib-summar/temporal-worker-controller/pull/79)) ([`ae0d5c9`](https://github.com/shazib-summar/temporal-worker-controller/commit/ae0d5c99792395f7c2a561eeadeb1aa986bafe45))
- address comments ([`41d65fa`](https://github.com/shazib-summar/temporal-worker-controller/commit/41d65fa84e7b695a6aae4f49b7ffda97c713109a))
- fix variable shadowing error ([`4246df6`](https://github.com/shazib-summar/temporal-worker-controller/commit/4246df6724852a38826a04c14a3b44e37e2ffd59))
- make it a team ([`aaacb54`](https://github.com/shazib-summar/temporal-worker-controller/commit/aaacb54d7ca5941c3e729f133183be349e3873fa))
- add codeowners file ([`d9c95d6`](https://github.com/shazib-summar/temporal-worker-controller/commit/d9c95d69d7e0916a33260987a209476bda103b12))
- go mod tidy ([`2e3844d`](https://github.com/shazib-summar/temporal-worker-controller/commit/2e3844dcc884cb2400b1455b477355fde1e010a8))
- update go module name ([`b21886b`](https://github.com/shazib-summar/temporal-worker-controller/commit/b21886bb375119c0ae8433b930739dc7ad3889a9))
- rm copyright tool ([`e61364a`](https://github.com/shazib-summar/temporal-worker-controller/commit/e61364ac0223bb176b8acc1ea57dca082e18ec58))
- rm licensecheck ([`2d79c63`](https://github.com/shazib-summar/temporal-worker-controller/commit/2d79c630d8517e7f6dd3bdb9a1261e8195092816))
- Bump github.com/golang/glog from 1.2.1 to 1.2.4 ([`ef18ee4`](https://github.com/shazib-summar/temporal-worker-controller/commit/ef18ee48922c3d0c6ea5864bb05663cea2a9581a))
- Bump golang.org/x/net from 0.33.0 to 0.38.0 in /internal/demo ([`65028dc`](https://github.com/shazib-summar/temporal-worker-controller/commit/65028dc7701de887d882594da1e0761862017429))
- Bump golang.org/x/crypto from 0.27.0 to 0.35.0 ([`f633e1e`](https://github.com/shazib-summar/temporal-worker-controller/commit/f633e1e552cf60ec86cb5ee78afc28a6b288d83d))
- unit test twd validation ([`91d0a3f`](https://github.com/shazib-summar/temporal-worker-controller/commit/91d0a3fe25ac40faa7058576da8aa5a70f0d2f46))
- current version + versionConflictToken not required to be nil ([`60f4f61`](https://github.com/shazib-summar/temporal-worker-controller/commit/60f4f6142f8eabc57d814b8df57dde531b0f14db))
- fix imports ([`5974167`](https://github.com/shazib-summar/temporal-worker-controller/commit/59741679571ffbf899c951081a0e061f49ff9929))
- address comments and make custom validator ([`96aad87`](https://github.com/shazib-summar/temporal-worker-controller/commit/96aad8771d6a0fdb618bcf0cfff97362ace5988c))
- Revert "Allow storing TestWorkflows on all deployment types." ([`d5052ac`](https://github.com/shazib-summar/temporal-worker-controller/commit/d5052aca7e30afd9771ec2b5bd1cac02f4ce2699))
- Allow storing TestWorkflows on all deployment types. ([`9730d59`](https://github.com/shazib-summar/temporal-worker-controller/commit/9730d5909bae7bfd4bca478d5e2c377a5f83def4))
- Update YAML. ([`d41bb83`](https://github.com/shazib-summar/temporal-worker-controller/commit/d41bb83de5db924eadd0e4e55246bba4883dd648))
- address comments ([`03b4a94`](https://github.com/shazib-summar/temporal-worker-controller/commit/03b4a94815be4b3ddf3846bdc1828edb914e6643))
- Refactor worker version types. ([`1e9b34a`](https://github.com/shazib-summar/temporal-worker-controller/commit/1e9b34a6fff675f5b7d43afff80b85fb56d4409d))
- Update internal/k8s/deployments.go ([`b9b4b2b`](https://github.com/shazib-summar/temporal-worker-controller/commit/b9b4b2b4a520f981903d86c3e7e2bbee97918d24))
- fix build ([`9f76aba`](https://github.com/shazib-summar/temporal-worker-controller/commit/9f76abaed716ba7a52a67541da191402b6dab21a))
- add cleaned up image prefix to build id ([`7e1464b`](https://github.com/shazib-summar/temporal-worker-controller/commit/7e1464b5378a3e45dea3d3f7882a1f860e411be9))
- using timeout of 5 minutes for a reconcilliation loop ([`f57ebce`](https://github.com/shazib-summar/temporal-worker-controller/commit/f57ebce0649feefc4c9a190fffed735586302da6))
- more comments ([`b341fcb`](https://github.com/shazib-summar/temporal-worker-controller/commit/b341fcbdf428934cad0b6aa8404b427af125d66f))
- update comment ([`0644a96`](https://github.com/shazib-summar/temporal-worker-controller/commit/0644a96ad0275c4870f1146b267ea3f9a3650a71))
- changes ([`7648557`](https://github.com/shazib-summar/temporal-worker-controller/commit/7648557c512f90e88b719475e983f2f85508b2d1))
- Store the original PEM-encoded certificate for expiration checks ([`6a04dbc`](https://github.com/shazib-summar/temporal-worker-controller/commit/6a04dbcc6b93f13ae081bd2e0b6d7a660795fd14))
- comments ([`248a9cc`](https://github.com/shazib-summar/temporal-worker-controller/commit/248a9cc338d230008c9e8a2362c0a6c6d1afbed4))
- reduce the expiring soon time to 5 minutes since that's the minimum time allowed before a cert refresh ([`96b3165`](https://github.com/shazib-summar/temporal-worker-controller/commit/96b316599831a92662b3f391b30d0f57cccfa8b5))
- add a buffer check for certs ([`7a5ef80`](https://github.com/shazib-summar/temporal-worker-controller/commit/7a5ef80da04804d78b2a4ec72330933504bf8860))
- restore non-required changes when comparing to main ([`8a982b0`](https://github.com/shazib-summar/temporal-worker-controller/commit/8a982b00ffd8cf0ba1339e660d76a056aa7841b3))
- sdkClient refresh when certs have expired ([`4c2b470`](https://github.com/shazib-summar/temporal-worker-controller/commit/4c2b47023df80db18d259ac503f9230c58bccc5b))
- added controller version and identity while updating the version metadata ([`a45fd35`](https://github.com/shazib-summar/temporal-worker-controller/commit/a45fd355972aa6936d0db2f84f92ac415a49e4ff))
- rm config dir ([`e245c0b`](https://github.com/shazib-summar/temporal-worker-controller/commit/e245c0bacf3dc07e7395dcdcb1b1891c9e80dfd9))
- update copyright ([`269e3d4`](https://github.com/shazib-summar/temporal-worker-controller/commit/269e3d4fee9edf5cfb86ce9afa4339c949117ebe))
- update third party deps ([`d76592d`](https://github.com/shazib-summar/temporal-worker-controller/commit/d76592d28da0c45b6a1600c32055cc0edafee66f))
- update demo go version ([`51d19ca`](https://github.com/shazib-summar/temporal-worker-controller/commit/51d19cafb4cc7fd6581f89df1b90e8974ffe107a))
- increase reconciliation concurrency ([`e1dd2ac`](https://github.com/shazib-summar/temporal-worker-controller/commit/e1dd2ac9bd04b6ff5697e01fcfd715b08fac9b87))
- dedupe labels ([`813adad`](https://github.com/shazib-summar/temporal-worker-controller/commit/813adad756a623f350dec1c123f76cef265c766e))
- make generate ([`63960a3`](https://github.com/shazib-summar/temporal-worker-controller/commit/63960a38a7ddd8b3016912d02a8bff2bda6e183c))
- make manifests ([`e10b185`](https://github.com/shazib-summar/temporal-worker-controller/commit/e10b18507027deabcb618875c9aa353ae0887131))
- Update go version & fix build ([`2b8b7d3`](https://github.com/shazib-summar/temporal-worker-controller/commit/2b8b7d37d57d6bac2c1fe339dd3a2d5f224fec7c))
- Further simplification. ([`6c12403`](https://github.com/shazib-summar/temporal-worker-controller/commit/6c1240368ef276c50c7620bd93f39f2ae7fbc7c4))
- Simplify exec. ([`c1530df`](https://github.com/shazib-summar/temporal-worker-controller/commit/c1530dfbe5a43fe8f7a086656b4815db55317721))
- Remove duplicated type. ([`4fadc1e`](https://github.com/shazib-summar/temporal-worker-controller/commit/4fadc1ea685595f39e8f4cf3e28997509fd4fdb7))
- Simplify based on feedback from Carly. ([`18a4ca3`](https://github.com/shazib-summar/temporal-worker-controller/commit/18a4ca3db0a49faa7d03d431300e3e31a26fd8d8))
- Rollback ramps on deprecated versions. ([`b0eaada`](https://github.com/shazib-summar/temporal-worker-controller/commit/b0eaada84e62f3576b27918d788afeeaa285694f))
- Update internal naming to use current, not default. ([`36dd74f`](https://github.com/shazib-summar/temporal-worker-controller/commit/36dd74f0950f20e3d834e75818f30b02216721eb))
- Restore TODOs. ([`4ede44f`](https://github.com/shazib-summar/temporal-worker-controller/commit/4ede44f3946058b0a8babdda03b738ac0dbe65ba))
- Correct a test which used an invalid setup. ([`57a0279`](https://github.com/shazib-summar/temporal-worker-controller/commit/57a0279958b90553c17e77a1ad89ec5b48f4c8f0))
- Extend test coverage. ([`0b0f1fd`](https://github.com/shazib-summar/temporal-worker-controller/commit/0b0f1fda45915e1b6d54da848c110cb7c1339840))
- Don't store a per-version ramp percentage. ([`4ae150a`](https://github.com/shazib-summar/temporal-worker-controller/commit/4ae150a257182a7f3c6e7a9cd70b8f0a7484fc67))
- Improve tests. ([`faa6402`](https://github.com/shazib-summar/temporal-worker-controller/commit/faa64028aaa5488f4db665a84e07ba2fb1cf02c7))
- Refactor genplan to split planning from orchestration. ([`459480c`](https://github.com/shazib-summar/temporal-worker-controller/commit/459480c00300f0d5c01fcb1225dacf9b1e1253a7))
- Fix broken merge. ([`d748903`](https://github.com/shazib-summar/temporal-worker-controller/commit/d748903bc1282d830d5bb84d70527e8c11a157fa))
- Add some godoc. ([`39f5447`](https://github.com/shazib-summar/temporal-worker-controller/commit/39f5447e9fd05b362148b1849bcb8aaf0848e958))
- Keep rollout logic in genplan. ([`d422b54`](https://github.com/shazib-summar/temporal-worker-controller/commit/d422b54425569149fbda9079db0e899940324669))
- Remove some exports which aren't needed. ([`b09b1a7`](https://github.com/shazib-summar/temporal-worker-controller/commit/b09b1a7a5d3e7f59e1a641b7738f50180cd34b27))
- Use v1alpha1 types where possible. ([`b7ee85e`](https://github.com/shazib-summar/temporal-worker-controller/commit/b7ee85ecb8016d8bd84220c1a5bdd76fdeae2ac2))
- Refactor genstatus for separation of concerns. ([`d1cb35e`](https://github.com/shazib-summar/temporal-worker-controller/commit/d1cb35e7e52e6ebcd31041c71d10f543c0911aa2))
- Better conflict protection. ([`1e05006`](https://github.com/shazib-summar/temporal-worker-controller/commit/1e050065c36a17e3649e2edcede4359509b61d38))
- Treat external modification like manual rollout strategy. ([`6c17dae`](https://github.com/shazib-summar/temporal-worker-controller/commit/6c17dae496ef49d076cf33fedb6d01b11b34f1da))
- Update certificate path. ([`41892db`](https://github.com/shazib-summar/temporal-worker-controller/commit/41892db5c872cd475867ba8041360e0fe12927ff))
- Fix missing whitespace. ([`95d54e7`](https://github.com/shazib-summar/temporal-worker-controller/commit/95d54e77f93723fe65ff992a7bc6b5775f85a82b))
- Adjust certificate names to avoid confusion. ([`64cddd7`](https://github.com/shazib-summar/temporal-worker-controller/commit/64cddd7db0c9f2d5972f6169365e12e6deb38524))
- Skip any deployments which have been touched by an external system. ([`fad4283`](https://github.com/shazib-summar/temporal-worker-controller/commit/fad42836e9bab2d5a28e26edfc47fcd0d5476c75))
- use helm in makefile ([`f93d192`](https://github.com/shazib-summar/temporal-worker-controller/commit/f93d192a76ad993008b935dbc48e1bcd5f80f685))
- replace kustomize with helm chart ([`3feeeb7`](https://github.com/shazib-summar/temporal-worker-controller/commit/3feeeb75ba42ccd8396efd143e5ecf9cc05bf812))
- remove duplicated code in test ([`db30082`](https://github.com/shazib-summar/temporal-worker-controller/commit/db3008222e3a263eac3bbd2275312c511f0b67c8))
- testNS -> testTemporalNS ([`2aa4034`](https://github.com/shazib-summar/temporal-worker-controller/commit/2aa4034a9d289b3719ea58bac7ae2439369c2f8d))
- VersionID -> foo/ns.buildID ([`4356802`](https://github.com/shazib-summar/temporal-worker-controller/commit/43568027186c939023b012fb877816b319703414))
- remove some outdated code: ([`2e115c9`](https://github.com/shazib-summar/temporal-worker-controller/commit/2e115c9f8e08491db471e2a59a0ab8104c3e30ba))
- revert naming from ns/deployment to deployment/ns ([`1061d1b`](https://github.com/shazib-summar/temporal-worker-controller/commit/1061d1b9398f65f3312f95e536b2f09f710484de))
- scaledown delay + deploymentName being formed as ns.Name/twd.Name ([`87be3d8`](https://github.com/shazib-summar/temporal-worker-controller/commit/87be3d848f9fd249b80f991358df9684c96f406b))
- use sdk client instead of gRPC client ([`398b59d`](https://github.com/shazib-summar/temporal-worker-controller/commit/398b59d4ea3e5caff31c17b3e6b3c2aa3a83e6ff))
- change deployment name back to foo/ns ([`93003e9`](https://github.com/shazib-summar/temporal-worker-controller/commit/93003e916a3edb4b6f5110626f5572e1af5be354))
- update README and naming conventions ([`7c3ac61`](https://github.com/shazib-summar/temporal-worker-controller/commit/7c3ac619c892482d537ec70ad921c6a6b65e96a3))
- make separator a constant ([`6fac4a1`](https://github.com/shazib-summar/temporal-worker-controller/commit/6fac4a19440f6be9102e13224c276acf7aa56d1c))
- add twd, twdeployment, tworkerdeployment aliases ([`e9471dd`](https://github.com/shazib-summar/temporal-worker-controller/commit/e9471dda9711d84cbd4ab4bdc3c9889226103b39))
- don't allow custom deployment name and label twd-controlled deployments correctly ([`98a4461`](https://github.com/shazib-summar/temporal-worker-controller/commit/98a4461405630cefcb4a8a550a6fc2c98f4d8f57))
- rename TemporalWorker CRD -> TemporalWorkerDeployment ([`0adc267`](https://github.com/shazib-summar/temporal-worker-controller/commit/0adc26702b577b5101f7d14243d74100d5102be6))
- Use v3.1 APIs ([`1bcac8c`](https://github.com/shazib-summar/temporal-worker-controller/commit/1bcac8ce4ff84f365c0649dc43a633be73128d18))
- Use v3 APIs ([`c232182`](https://github.com/shazib-summar/temporal-worker-controller/commit/c2321829bff7c9b6a147ca2891e1b0fd2ab3b1f1))
- Remove stats from worker status ([`fecc5c1`](https://github.com/shazib-summar/temporal-worker-controller/commit/fecc5c12065510edf4a242222c89676169abeccf))
- Support Temporal v1.25.0 ([`6d4cc86`](https://github.com/shazib-summar/temporal-worker-controller/commit/6d4cc8642ee14301de233ad8f8d506c874ace74f))
- Run licensecheck and copyright ([`e1b2c55`](https://github.com/shazib-summar/temporal-worker-controller/commit/e1b2c556dbe37e613c9b78f4042d0ec49635a3dc))
- Add licensecheck and copyright tools ([`8eb96e5`](https://github.com/shazib-summar/temporal-worker-controller/commit/8eb96e5e1badf4f6f1873c17835baf249e1cbc22))
- Rename module ([`1c30850`](https://github.com/shazib-summar/temporal-worker-controller/commit/1c308508d8c862760c6c86c60431181c6afad659))
- Working proof of concept ([`75ec75b`](https://github.com/shazib-summar/temporal-worker-controller/commit/75ec75badc9e4eec0ca68066cb98707ac6b3bef4))
- Add TODO about generated name length ([`0432c50`](https://github.com/shazib-summar/temporal-worker-controller/commit/0432c50fc306b67852c21d198b7b333d032e8786))
- Add test cases for generatePlan ([`7ffcabe`](https://github.com/shazib-summar/temporal-worker-controller/commit/7ffcabe891fadfbfa18f0851a749ce62ab980277))
- Differentiate between currently deployed and default build IDs ([`9d09f7b`](https://github.com/shazib-summar/temporal-worker-controller/commit/9d09f7b50acc0bab5b73180262666d9b5479e6f9))
- Generate list of unreachable deployments ([`c2ebfde`](https://github.com/shazib-summar/temporal-worker-controller/commit/c2ebfde7fd89680b244a84abc430f44b0e5c95f7))
- Initial commit ([`4901501`](https://github.com/shazib-summar/temporal-worker-controller/commit/490150134eeede1be89d954d23ef5f8fc75f64a6))
