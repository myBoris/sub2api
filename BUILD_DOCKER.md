# Docker 镜像打包与推送

当前源码版本为 0.2.24，已包含账号支付、续费到期限制及精简支付页面。重新发布时应从对应 Git 提交构建，并将提交号和构建时间写入镜像版本信息。

该源码包含上游迁移 `252_channel_pricing_ultrafast_multiplier.sql`，会为 `channel_model_pricing` 新增字段及约束。现有本机服务使用的 0.2.22 兼容镜像没有此迁移；如需在保留原表结构的约束下更新现有服务，应先做单独的数据库兼容处理。构建及上传镜像本身不执行数据库迁移。

此前于 2026-11-22 发布的兼容版本为 `ancodej/sub2api:account-payment-no-hint-20261122`，远端摘要为 `sha256:e0fc08cc3981ba8414f4d5d347b4c8cc4c0fe39e7c74d649894dedc44e65f219`。历史发布记录位于 `G:\work\api\artifacts\docker-publish-20261122\publication.json`。

在 `sub2api` 源码目录使用独立的 `docker-compose.build.yml`，将当前工作目录中的源码打包为 `ancodej/sub2api:latest`。此流程只负责构建和推送，无需部署目录、`.env` 或数据库。

先启动 Docker Desktop，并使用 Linux 容器。在 PowerShell 中执行：

```powershell
Set-Location 'G:\work\api\sub2api'

$env:SUB2API_BUILD_VERSION = (Get-Content 'backend\cmd\server\VERSION' -Raw).Trim()
$env:SUB2API_BUILD_COMMIT = (git rev-parse --short HEAD).Trim()
$env:SUB2API_BUILD_DATE = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

docker compose -f docker-compose.build.yml build sub2api
```

构建成功后，在同一个 PowerShell 窗口登录 Docker Hub，按提示使用 `ancodej` 账号完成网页授权，再推送：

```powershell
docker login
docker compose -f docker-compose.build.yml push sub2api
```

每次发布都会替换 Docker Hub 上的 `ancodej/sub2api:latest`。版本号、提交号和 UTC 构建时间写入应用版本信息；上述环境变量仅对当前 PowerShell 会话生效。

默认目标平台为 `linux/amd64`。若部署服务器使用 ARM64，在构建命令执行前设置：

```powershell
$env:SUB2API_BUILD_PLATFORM = 'linux/arm64'
```

推送完成后，部署端按独立部署流程启动或更新应用。源码目录和打包配置无需复制到部署服务器。
