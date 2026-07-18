# SeedGraph

[![CI](https://github.com/lesir831/SeedGraph/actions/workflows/ci.yml/badge.svg)](https://github.com/lesir831/SeedGraph/actions/workflows/ci.yml)

SeedGraph 是一个单用户、自托管的 BT 任务管理面板。它把多个 qBittorrent 和 Transmission 实例中的任务汇总到一起，识别逻辑上的同一内容与物理上的同一份数据，并在删除前执行引用检查和状态复验。

> **删除会调用下载器并可能移除真实文件。** SeedGraph 会生成预览、阻止不确定操作并在执行前重新同步，但它不能替代下载目录的独立备份。第一次使用删除功能前，请先在非关键数据上验证下载器地址、存储身份和路径映射。

## 功能

- 聚合 qBittorrent 与 Transmission 的任务、状态、流量和 Tracker 信息。
- 按规范化路径、大小和已选文件清单建立自动分组。
- 支持逻辑分组的手动合并、拆分、成员移动、锁定、恢复自动分组和并发安全撤销。
- 通过两阶段“删除预览 → 删除任务”流程执行安全删除。
- 管理下载器、物理存储身份、跨主机路径映射和 Tracker 归类规则。
- 每日同步 IYUU 站点元数据目录；失败时保留上次成功快照，且不会把网站域名误当作 Tracker announce 域名。
- 记录同步状态、删除进度和审计事件。
- 单个 Go 进程提供 JSON API 与 React 管理界面，SQLite 保存应用状态。

SeedGraph 始终区分两种分组：

| 模型 | 用途 | 是否决定删除文件 |
| --- | --- | --- |
| `ContentGroup` | 展示、筛选、站点计数，以及用户定义的逻辑关系 | 否 |
| `DataGroup` | 表示同一物理存储上的同一份数据，用于引用计数 | 是 |

手动合并 `ContentGroup` 不会合并 `DataGroup`，因此“看起来是同一内容”不会自动变成“可以安全删除同一份文件”。详细设计见 [架构说明](docs/architecture.md)。

## Docker Compose 快速开始

需要 Docker Engine 与 Docker Compose v2。

1. 创建本地配置：

   ```bash
   cp .env.example .env
   openssl rand -base64 32
   ```

2. 编辑 `.env`，至少替换以下两个示例值：

   - `SEEDGRAPH_ADMIN_PASSWORD`：管理密码，至少 8 个字符。
   - `SEEDGRAPH_SECRET_KEY`：粘贴上一步生成的随机值；原始值或 Base64 解码后必须至少 32 字节。

3. 构建并启动：

   ```bash
   docker compose up -d --build
   docker compose ps
   ```

4. 打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)，使用用户名 `admin` 和 `.env` 中的密码登录。健康检查地址是 `http://127.0.0.1:8080/healthz`。

默认只监听宿主机回环地址。如需让同一网络中的其他设备访问，可在 `.env` 增加 `SEEDGRAPH_BIND_ADDRESS=0.0.0.0`。面向非可信网络时，请使用 HTTPS 反向代理，并设置 `SEEDGRAPH_COOKIE_SECURE=true`。

应用数据库位于 Compose 命名卷 `seedgraph-data` 中的 `/data/seedgraph.db`。升级或迁移前应备份该数据库和 `SEEDGRAPH_SECRET_KEY`。更换密钥会使已有会话失效，并使已加密的下载器凭据无法解密。

## 添加下载器

登录后进入“下载器”页面：

1. 选择 qBittorrent 或 Transmission，并填写 SeedGraph 能访问的 Web UI/RPC 地址。
2. 为下载器选择或新建一个存储身份。同一物理存储上的多个下载器必须使用同一存储身份；不同磁盘或不同副本必须使用不同身份。
3. 如果不同下载器看到的挂载路径不同，添加绝对路径映射。例如把 `/downloads` 映射到 `/srv/torrents`。
4. 先测试连接，再执行同步。确认分组结果后再使用删除功能。

容器访问宿主机下载器时，Docker Desktop 通常可使用 `host.docker.internal`。Linux 环境应使用宿主机可达地址，或按部署环境配置 host gateway/反向代理。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SEEDGRAPH_LISTEN_ADDR` | `:8080` | HTTP 监听地址。Compose 固定为容器内 `:8080`。 |
| `SEEDGRAPH_DATABASE_PATH` | `data/seedgraph.db` | SQLite 文件路径。Compose 使用 `/data/seedgraph.db`。 |
| `SEEDGRAPH_WEB_DIR` | `frontend/dist` | 前端静态文件目录。Compose 使用 `/app/web`。 |
| `SEEDGRAPH_ADMIN_PASSWORD` | 无 | 必填，至少 8 个字符。 |
| `SEEDGRAPH_SECRET_KEY` | 无 | 必填，原始值或 Base64 解码后至少 32 字节。用于会话签名与下载器凭据加密。 |
| `SEEDGRAPH_COOKIE_SECURE` | `false` | HTTPS 部署时设为 `true`。 |
| `SEEDGRAPH_SYNC_INTERVAL` | `30s` | 增量同步调度间隔。 |
| `SEEDGRAPH_FULL_SYNC_INTERVAL` | `30m` | 完整同步间隔，不能短于增量同步间隔。 |
| `SEEDGRAPH_STALE_AFTER` | `5m` | 超过该时间未成功同步的状态视为过期，并阻止依赖它的删除。 |
| `SEEDGRAPH_IYUU_SYNC_ENABLED` | `true` | 是否在启动后及每日定时同步 IYUU 公开站点目录。手动同步 API 仍可用。 |
| `SEEDGRAPH_IYUU_SYNC_INTERVAL` | `24h` | IYUU 目录定时同步间隔；调度会附加少量正向抖动。 |
| `SEEDGRAPH_IYUU_SITES_URL` | `https://2025.iyuu.cn/reseed/sites/index` | IYUU 站点目录端点；不携带下载器或 Tracker 凭据。 |
| `SEEDGRAPH_BIND_ADDRESS` | `127.0.0.1` | 仅供 Compose 使用的宿主机端口绑定地址。 |

时长使用 Go duration 格式，例如 `30s`、`5m`、`1h`。

首次启动后，`SEEDGRAPH_ADMIN_PASSWORD` 只用于创建管理员密码哈希；修改已有部署的该环境变量不会自动重置数据库中的密码。IYUU 同步会访问上表中的公开目录端点；不需要此能力时可把 `SEEDGRAPH_IYUU_SYNC_ENABLED` 设为 `false`。

## 本地开发

需要 Go 1.24、Node.js 24 和 npm。Node 版本同时记录在 `.nvmrc` 与 `.node-version` 中。

```bash
make bootstrap
cp .env.example .env
# 编辑 .env 中的密码和密钥，然后载入当前 shell
set -a
source .env
set +a
export SEEDGRAPH_DATABASE_PATH=data/seedgraph.db
export SEEDGRAPH_WEB_DIR=frontend/dist
make check
make run
```

`make run` 在 `http://127.0.0.1:8080` 提供构建后的界面。前端热更新可在另一个终端运行 `make frontend`，Vite 会把 `/api` 代理到 `http://localhost:8080`；可用 `VITE_DEV_PROXY` 覆盖目标地址。

常用目标：

| 命令 | 作用 |
| --- | --- |
| `make fmt` | 格式化 Go 源码。 |
| `make lint` | 运行 `go vet`、ESLint 和 TypeScript 类型检查。 |
| `make test` | 运行 Go 与前端测试。 |
| `make test-race` | 运行 Go race detector。 |
| `make build` | 构建 `dist/seedgraph` 与前端静态资源。 |
| `make docker` | 构建 Compose 镜像。 |
| `make check` | 依次执行 lint、test 和 build。 |

## CI 与发布

推送和拉取请求会运行 Go 模块校验、格式检查、golangci-lint、race tests、前端 lint/typecheck/tests/build，以及容器构建和 `/healthz` 冒烟测试。

`main` 分支通过全部检查后会构建 `linux/amd64` 与 `linux/arm64` 镜像，并推送到 GitHub Container Registry：

```bash
docker pull ghcr.io/lesir831/seedgraph
```

`latest` 与 `edge` 始终指向最新的已通过检查的 `main` 提交；工作流还会发布 `sha-<commit>` 标签并生成 SBOM 与来源证明，便于固定和验证具体构建。

包历史中形如 `sha256-<digest>` 的条目是来源证明对象，不是可运行镜像标签。固定镜像摘要时使用 `@sha256:<digest>`，例如 `docker pull ghcr.io/lesir831/seedgraph@sha256:...`，不要把摘要改写成冒号后的标签。

GitHub 新建的容器包默认是私有的。保持私有时，先使用具备 `read:packages` 权限的令牌登录 `ghcr.io`；如需匿名拉取，请在首次发布后到包设置中把可见性改为 Public。公开包后不能再改回私有。

推送符合 SemVer 的标签（例如 `v0.1.0` 或 `v0.1.0-rc.1`）会触发稳定版发布工作流：再次验证前后端，推送版本标签和提交标签，并创建 GitHub Release。版本发布不会覆盖由 `main` 维护的 `latest` 与 `edge`。

## API 与安全边界

- API 前缀：`/api/v1`
- 健康检查：`/healthz`
- 当前只支持单个 `admin` 用户，不提供多租户权限隔离。
- 会话保存在 HttpOnly、SameSite=Strict Cookie 中；修改请求还要求 CSRF token。
- 下载器凭据在 SQLite 中加密保存，Tracker passkey 不会作为站点身份持久化。
- IYUU 目录与 Tracker 规则分开保存：目录只用于站点名称/网站元数据，Tracker 分类仍以显式自定义规则为准。

## License

[MIT](LICENSE)
