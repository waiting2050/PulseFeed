# PulseFeed

按领域分层的轻量内容流系统：覆盖发布、四场景 Feed、互动、向量推荐、消息与播放体验的完整链路。

[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=white)](https://react.dev/)
[![Node](https://img.shields.io/badge/Node-20-339933?logo=node.js&logoColor=white)](https://nodejs.org/)
[![MySQL](https://img.shields.io/badge/MySQL-8.4-4479A1?logo=mysql&logoColor=white)](https://www.mysql.com/)
[![CI](https://github.com/waiting2050/PulseFeed/actions/workflows/ci.yml/badge.svg)](https://github.com/waiting2050/PulseFeed/actions/workflows/ci.yml)

## 目录

- [核心能力](#核心能力)
- [架构](#架构)
- [快速开始](#快速开始)
- [截图](#截图)
- [接口速览](#接口速览)
- [测试与校验](#测试与校验)
- [当前进度](#当前进度)
- [文档索引](#文档索引)

## 核心能力

| 能力 | 说明 |
|---|---|
| Feed 四场景 | `latest` 按发布时间倒序；`hot` 合并 Redis 分钟桶热度；`following` 来自关注创作者；`for-you` 走向量召回与加权排序。 |
| 游标分页 | 所有列表使用不透明游标续翻，避免 offset 漂移；`for-you` 游标携带候选池快照与排序分数，跨分钟翻页不重不漏。 |
| Redis 缓存 | Feed 页、卡片与互动计数缓存；热度按分钟桶累加（最近 60 个桶，约 1 小时窗口）。 |
| 互动异步落库 | 点赞/收藏先写 Redis，再投递 `pulsefeed.interaction.action_changed`，由 Worker 落库；Redis 或 MQ 不可用时回退同步写库。 |
| 失败重试与死信 | 四组消费队列均配置死信交换机与死信队列；消费失败先回原队列，同一进程累计 3 次后转入死信，不可重试错误直接进死信。 |
| 展示与播放上报 | `exposed` / `play` / `skip` / `complete` 四类事件，携带 `scene`、`request_id` 与 `watch_ms`；`play_count` 只在 `event_type=play` 且 `event_id` 首次落库时累加，其余事件只更新展示聚合。 |
| 播放事件幂等 | `event_id` 唯一键拦住 `pf_play_events` 重复写入；同一 `event_id` 的 QoS 上报不会二次累加 `play_count`。 |
| 向量召回 | 发布后由 Embedding Worker 生成 256 维 hash n-gram 向量；`for-you` 以读者互动内容的兴趣向量做余弦相似度加权。 |
| 消息中心 | 点赞、评论、关注与系统四类通知，消息带标题与正文；未读状态由 `read_at` 派生，支持全部已读与按 ID 逐条已读。 |
| 个人主页与关注 | `/me` 与 `/users/:userId` 展示资料、作品及关注/粉丝数；Feed 卡片内可直接关注，关注/粉丝列表以弹层展开。关注自己返回 400，重复关注与取关幂等。 |
| 发布简介 | 发布时可选填简介，`TEXT` 存储、上限 1000 字符（按码点），超限返回 400 且不截断；Feed、详情与个人主页作品列表均返回该字段。 |
| 上传硬校验 | 媒体单文件 512MB、图片 20MB、时长 10 分钟、长边 3840，编码白名单 H.264 / HEVC / VP9；校验顺序为大小 → 扩展名 → 内容探测，失败不落盘或落盘后删除。`ffprobe` 缺失时降级为扩展名校验并在日志写明跳过项。 |
| 播放配置与 QoS | `preload_count`、`qos_sample_rate`、`buffer_ms` 可按用户读写；`GET /api/preload-clips` 返回 `clip_id` / `media_url` / `cover_url` / `duration_ms`，前端首屏后并发 3 预取封面与媒体元信息，切换场景或离开时取消，失败静默。QoS 上报首帧延迟、卡顿次数、实际播放时长与完播状态。 |
| 监控指标 | API 与 Worker 使用独立 Prometheus Registry，指标前缀 `pulsefeed_`；`monitoring` profile 提供 Prometheus 与 Grafana。 |
| 容器化 | Docker Compose 编排全栈；宿主端口可由 `.env` 覆盖。另附 K8s 清单，未做集群级实测。 |

## 架构

请求链路：

```mermaid
flowchart LR
  B["浏览器"] --> N["nginx<br/>静态资源 + /api /uploads 反代"]
  N --> A["API（Gin）<br/>:8090"]
  A --> M[("MySQL 8.4")]
  A --> R[("Redis 7.4")]
  A --> Q[("RabbitMQ 3.13")]
  Q --> W["Worker<br/>Action / Fanout / Embedding / PlayEvent"]
  W --> M
  W --> R
```

- API 进程负责鉴权、读写接口、静态上传回读与指标暴露；Worker 独立消费四组队列，进程内累计失败次数并配合 broker 完成死信分流。
- 分层依赖方向为 `interfaces → application → domain`；`infra` 实现 `domain` 端口并在组合根注入，`application` 不直接依赖 `infra`。

目录结构：

```text
pulsefeed/
├── README.md
├── .github/workflows/ci.yml       # 后端 build/vet/test 与前端 build
├── docs/
│   ├── architecture.md            # 分层边界、关键数据流、死信与 QoS 口径
│   ├── frontend-redesign.md       # 前端视觉与交互重构记录
│   ├── routes-snapshot.md         # HTTP 路由、鉴权与端口快照
│   ├── prototype/                 # 设计方向原型
│   └── screenshots/               # README 截图
├── scripts/
│   ├── start.sh                   # 本地三进程启动与信号清理
│   └── acceptance-tests.ps1
└── apps/
    ├── api/                       # Go module（module: pulsefeed）
    │   ├── cmd/
    │   │   ├── api/               # HTTP 组合根
    │   │   └── worker/            # MQ 消费者组合根
    │   ├── configs/               # config.yaml / config.docker.yaml
    │   ├── internal/
    │   │   ├── domain/            # 实体、错误哨兵、仓储端口
    │   │   ├── application/       # 用例编排与 Worker
    │   │   ├── infra/             # MySQL / Redis / RabbitMQ / JWT / 指标实现
    │   │   └── interfaces/http/   # handler、dto、路由、中间件
    │   └── test/integration/      # build tag 集成测试
    ├── web/                       # React 18 + Vite 5
    ├── monitoring/                # Prometheus 配置与 Grafana provisioning
    ├── .env.example               # 宿主端口覆盖模板
    ├── docker-compose.yml
    └── deploy.yaml                # K8s 清单
```

技术栈：Go 1.22 + Gin、MySQL 8.4 + GORM AutoMigrate、Redis 7.4、RabbitMQ 3.13、JWT HS256（`Authorization: Bearer`）、内部接口 `X-Internal-Token`、React 18 + Vite 5 + react-router-dom 6。

## 快速开始

### 方式一：本机启动

前置依赖：MySQL `3306`、Redis `6379`、RabbitMQ `5672`；连接参数位于 `apps/api/configs/config.yaml`，首次启动自动迁移。

```bash
cd pulsefeed/apps/api
go run ./cmd/api

# 异步 Worker；本机 9092 可能被 Win11 Hyper-V 保留段占用，可显式覆盖指标端口
cd pulsefeed/apps/api
go run ./cmd/worker -metrics-addr :19092

cd pulsefeed/apps/web
npm install
npm run dev            # http://127.0.0.1:5173
```

前端开发态把 `/api`、`/uploads` 代理到 `http://127.0.0.1:8090`，目标地址可用 `PULSEFEED_API` 覆盖。

健康检查与指标：`GET :8090/health`、`GET :8090/metrics`；Worker 使用上面的 `-metrics-addr` 时为 `GET :19092/metrics`（缺省 `:9092`）。

> 直接复用 Compose 依赖时，注意本机配置与容器端口的差异：Compose 默认把 MySQL/Redis/RabbitMQ 映射到 `3307`/`6380`/`5673`，而 `config.yaml` 使用 `3306`/`6379`/`5672`，需要改配置或改映射后保持一致。

### 方式二：Docker Compose

```bash
cd pulsefeed/apps
cp .env.example .env          # Windows PowerShell: Copy-Item .env.example .env
docker compose up -d --build
# 打开 http://127.0.0.1:8088
```

只启动依赖或单独重建某个服务：

```bash
docker compose up -d mysql redis rabbitmq
docker compose up -d --build api worker web
docker compose --profile monitoring up -d prometheus grafana
docker compose --profile monitoring down   # 停止容器并保留数据卷
```

宿主端口全部可覆盖，模板见 `apps/.env.example`（复制为 `apps/.env` 后生效，`.env` 不提交）：

| 服务 | 默认宿主端口 | 覆盖变量 | 容器端口 | 说明 |
|---|---|---|---|---|
| web | 8088 | `WEB_PORT` | 80 | nginx 托管构建产物，反代 `/api/`、`/uploads/` |
| api | 8090 | `API_PORT` | 8090 | HTTP API 与 `/health`、`/metrics` |
| worker | 19092 | `WORKER_METRICS_PORT` | 9092 | 指标端口，默认值避开 Win11 Hyper-V 保留段 9020–9119 |
| mysql | 3307 | `MYSQL_PORT` | 3306 | |
| redis | 6380 | `REDIS_PORT` | 6379 | |
| rabbitmq | 5673 / 15673 | `RABBITMQ_PORT` / `RABBITMQ_UI_PORT` | 5672 / 15672 | 15673 为管理台 |
| prometheus | 19090 | `PROMETHEUS_PORT` | 9090 | 仅 `monitoring` profile |
| grafana | 3000 | `GRAFANA_PORT` | 3000 | 仅 `monitoring` profile，预置数据源与仪表盘 |

端口冲突时不用改 `docker-compose.yml`：

```bash
cd pulsefeed/apps
cp .env.example .env        # Windows PowerShell: Copy-Item .env.example .env
# 编辑 .env，例如 REDIS_PORT=6381、MYSQL_PORT=3308
docker compose config --quiet
docker compose up -d --build
```

容器内端口与服务名固定（`mysql:3306`、`redis:6379`、`rabbitmq:5672`），改宿主映射不影响服务间通信。可用 `netsh interface ipv4 show excludedportrange protocol=tcp` 查看 Windows 当前保留段。监控验收地址为 `http://localhost:19090`（Prometheus）与 `http://localhost:3000`（Grafana，默认 `admin` / `pulsefeed`）。

## 截图

以下截图来自当前代码的本地 Compose 运行结果（`docker compose up -d --build`，浏览器访问 `http://127.0.0.1:8088`），未做后期合成。

### 登录

![登录页](docs/screenshots/login.png)

### Feed（桌面三列）

![Feed 桌面三列](docs/screenshots/feed.png)

### 详情弹层（含播放器）

![详情弹层与播放器](docs/screenshots/detail.png)

### 消息

![消息中心](docs/screenshots/messages.png)

### 播放配置

![播放配置](docs/screenshots/playback.png)

## 接口速览

统一响应信封 `{code, message, data}`，成功 `code=0`。以下只列主要端点，字段、请求示例与错误语义见 [路由与端口快照](docs/routes-snapshot.md)。

**账号与资料**

- `POST /api/users`、`POST /api/sessions`（公开）
- `DELETE /api/sessions/current`、`GET/PATCH /api/users/me`（登录）
- `GET /api/users/:userId`、`GET /api/users/:userId/clips`（公开）
- `GET /api/users/:userId/relation`（可选登录）、`GET /api/users/:userId/following`、`GET /api/users/:userId/followers`（公开）

**Feed**

- `GET /api/feed?scene=latest|hot|following|for-you`（可选登录）
- `POST /api/feed-search`（可选登录）

**发布与上传**

- `POST /api/clips`、`DELETE /api/clips/:clipId`（登录，写请求带 `Idempotency-Key`）
- 发布请求字段：`title`（必填，128 字符）、`description`（可选，1000 字符）、媒体地址、`cover_url`、`duration_ms`（字段名见接口快照）
- `GET /api/clips/:clipId`（公开）、`GET /api/users/me/clips`（登录）
- `POST /api/uploads`（登录，`kind` 区分媒体 / 封面 / 头像，取值见接口快照；硬校验口径见下方「上传硬校验」）

**互动**

- `PUT/DELETE /api/clips/:clipId/like`、`PUT/DELETE /api/clips/:clipId/save`（登录）
- `POST /api/clips/:clipId/comments`（登录）、`GET /api/clips/:clipId/comments`（公开）、`DELETE /api/comments/:commentId`（登录）

**播放**

- `POST /api/clip-play-events`（登录；`event_type` 取 `exposed` / `play` / `skip` / `complete`，带 `scene`、`request_id`、`watch_ms`）
- `POST /api/playback-qos-reports`（登录）
- `GET/PUT /api/playback-config`（登录；含 `preload_count`、`qos_sample_rate`、`buffer_ms`）
- `GET /api/preload-clips`（登录；返回 `clip_id` / `media_url` / `cover_url` / `duration_ms`，前端首屏预取使用）

**关注与消息**

- `PUT/DELETE /api/users/me/following/:targetUserId`、`GET /api/users/me/following`、`GET /api/users/me/followers`（登录）
- `GET /api/messages`（登录，返回标题、正文与派生 `read`）
- `GET /api/message-stats/unread`（登录）
- `PATCH /api/messages`（登录；传 `{ids:[...]}` 按条已读，不传 `ids` 为全部已读）

**内部接口**

- `/internal/*`（`X-Internal-Token`）

## 测试与校验

```bash
cd pulsefeed/apps/api
gofmt -l .            # 工作区为 CRLF 时需按 LF 归一化复核
go build ./...
go vet ./...
go test ./...

cd ../web
npm run build
```

禁用字样自查：对仓库执行关键词检索，输出为空即通过；具体关键词不在文档中重复。

### 集成测试（需真实 MySQL / Redis / RabbitMQ）

用 build tag 隔离，默认 `go test ./...` 不编译该目录。每次运行创建独立 schema（`pulsefeed_it_<timestamp>`），跑完自动 `DROP`，不读写演示数据。

```bash
cd pulsefeed/apps
docker compose up -d mysql redis rabbitmq   # 端口被占用时先按上文用 .env 换端口

cd api
# PowerShell 用 $env:VAR='...' 逐条设置；默认端口对应 compose 默认值
PULSEFEED_IT_MYSQL_DSN='root:pulsefeed-root@tcp(127.0.0.1:3307)/?charset=utf8mb4&parseTime=True&loc=Local' \
PULSEFEED_IT_REDIS_ADDR='127.0.0.1:6380' \
PULSEFEED_IT_RABBITMQ_URL='amqp://pulsefeed:pulsefeed@127.0.0.1:5673/' \
go test -tags=integration ./test/integration -v -count=1
```

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PULSEFEED_IT_MYSQL_DSN` | `root:pulsefeed-root@tcp(127.0.0.1:3307)/...` | 需 root 权限创建/删除独立 schema |
| `PULSEFEED_IT_REDIS_ADDR` | `127.0.0.1:6380` | 仅连通性检查 |
| `PULSEFEED_IT_RABBITMQ_URL` | `amqp://pulsefeed:pulsefeed@127.0.0.1:5673/` | 仅连通性检查 |

排障：先 `docker compose ps` 确认依赖为 healthy；`CREATE DATABASE` 报权限不足时检查 DSN 是否使用 root；连接超时优先核对 `.env` 实际映射端口与上述变量是否一致。

覆盖范围：`play_count` 跨表同 `event_id` 去重（含两个 goroutine 并发上报）、latest 场景逐页游标边界（含跨分钟）、死信路由（不可重试直接入死信、连续失败 3 次后入死信、原队列无残留）。

### 字符计数口径

- 后端按字符（Go `utf8.RuneCountInString`，Unicode 码点）计数：标题上限 128、简介上限 1000、昵称上限 64、评论上限 1000，均先 `TrimSpace` 再判定；标题、昵称与评论与列宽一致，简介为 `TEXT` 列，上限由应用层约束，不额外放宽。
- 前端发布页标题按码点展示 `X/120`，沿用既有 120 字提示上限；HTML `maxLength` 取 128（浏览器按 UTF-16 单元计数，不会少于码点数），因此不会出现前端放行、后端拒绝。昵称输入同步限制 64；简介按码点展示 `X/1000`，超限时禁用提交并提示。

### 媒体时长与首帧封面

- 上传媒体文件时调用 `ffprobe` 读取时长（写入 `pf_clips.duration_ms`，毫秒），调用 `ffmpeg` 抽取首帧写入 `uploads/cover/` 并作为 `cover_url` 回传；前端卡片按 `mm:ss` 展示时长角标，`duration_ms=0` 时不占位。
- 工具缺失或执行失败时只记录 `warning` 并降级为「无时长、无封面」，上传与发布照常成功；前端回退为暗色首字占位封面。
- API 镜像（`apps/api/Dockerfile`）已内置 `ffmpeg`，本地 Windows 不安装也能运行。

## 当前进度

以下清单逐条对照代码与本地运行结果核实；未实现或未验证的能力不勾选。

- [x] 后端主链路：账号、Clip 发布（`Idempotency-Key` 幂等）、Feed 四场景、互动、关注、推荐、热榜、消息、播放配置与 Prometheus 指标。
- [x] 互动异步落库：点赞/收藏先写 Redis，再投递 `pulsefeed.interaction.action_changed`，由 Worker 的 `ActionWorker` 落库；Redis 或 MQ 不可用时回退同步落库。
- [x] 播放事件幂等：`event_id` 唯一键拦住 `pf_play_events` 重复写入；与 `pf_playback_qos_reports` 同 `event_id` 时不再累加 `play_count`。集成测试覆盖并发去重。
- [x] 前端：登录 / Feed / 发布 / 消息 / 播放配置 / 我的主页 / 公开主页走独立路由；`/feed?scene=latest|hot|following|for-you` 直达场景，`/clip/:id?from=...` 为深链弹层，刷新、前进后退与直接粘贴 URL 均正常，关闭后回到来源列表；分页为「加载更多」按钮，不是无限滚动。
- [x] 播放器：卡片与详情内联播放器支持播放 / 暂停 / 静音 / 进度 / 全屏，并按首帧延迟、卡顿次数、播放时长、完播状态上报 QoS（`event_id` 幂等）；上报前按 `qos_sample_rate` 做会话级抽样。
- [x] 展示与播放上报：Feed 卡片进入视口上报 `exposed`，开始播放上报 `play`，播完上报 `complete`，展示后未播放即划走上报 `skip`；同一批次共用 `request_id`，展示与划走按 `(request_id, clip_id)` 去重，仅登录态上报。
- [x] 预加载：`GET /api/preload-clips` 按用户配置返回 `clip_id` / `media_url` / `cover_url` / `duration_ms`；前端首屏数据就绪后并发 3 预取封面与媒体元信息，场景切换或离开页面时统一取消，失败静默，不阻塞渲染。
- [x] 个人主页与关注：`/me` 与 `/users/:id` 展示资料、作品、关注/粉丝数与关注按钮；Feed 卡片作者可进入公开主页并在卡片内关注；关注/粉丝列表以弹层展示。关注自己 400，重复关注计数不重复累加，取关不存在的关注关系 200。
- [x] 消息四类通知与按条已读：点赞、评论、关注事件在状态真正生效时投递通知，系统消息走内部入口；消息表新增标题与正文，已读复用 `read_at` 派生布尔值（不新增 `is_read` 列），`PATCH /api/messages` 支持按 ID 逐条已读，不传 ID 时保持全部已读。
- [x] 发布简介：发布接口新增可选 `description`，`TEXT` 列，按码点上限 1000；超限返回 400 且不截断，Feed、详情与个人主页作品列表返回该字段。
- [x] 上传硬校验：媒体单文件 512MB、图片 20MB、时长 10 分钟、长边 3840，编码白名单 H.264 / HEVC / VP9；校验顺序为大小 → 扩展名 → `ffprobe` / 图片解码，失败不落盘或落盘后删除；`ffprobe` 缺失时降级为扩展名校验并在日志写明跳过项。
- [x] 容器化与监控：Compose 编排、宿主端口可由 `.env` 覆盖；Prometheus/Grafana 位于 `monitoring` profile；K8s 清单已提供但未做集群级实测。
- [x] 测试与 CI：`go build/vet/test`、前端 `npm run build` 与 GitHub Actions；真实依赖集成测试通过 `-tags=integration` 手动运行。
- [x] 媒体探测：上传媒体文件时由 `ffprobe` / `ffmpeg` 读取时长与首帧，工具缺失或失败时降级，不影响发布。
- [x] RabbitMQ 重投递与死信：四组消费队列均声明 `x-dead-letter-exchange`，对应死信交换机 `pulsefeed.<域>.dlx` 与死信队列 `<原队列>.dead`；消费失败先 `basic.nack(requeue=true)` 回原队列，同一消费进程累计失败 3 次（含首次）后 nack 不重入原队列，由 broker 转进死信；不可重试错误直接进死信，不消耗重试次数。旧队列参数不一致时 broker 返回 406 PreconditionFailed，Worker 中止启动并提示删除旧队列。
- [ ] 前端自动化：不做。目前只有构建校验，没有组件测试或 E2E，交互回归仍靠手工。

### 已知限制

- 契约字段保留原名：`video_url`、`kind=video`、`<video>` 等既有对外字段与标签不做重命名；词汇约束只适用于新增命名与文案，目的是避免出现项目名与可追溯措辞，不是待办事项。
- 上传不做 faststart 强制校验：静态路由已支持 Range，解析 moov/mdat 顺序对 mov/webm 容易误判，收益与实现成本不成比例，本轮不纳入校验。
- WebP 只做容器级校验：Go 标准库没有 WebP 解码器，当前校验 RIFF 容器声明与 VP8/VP8L/VP8X 块，不解码像素。
- `buffer_ms` 仅全局维度：按平台或网络维度细分需要调整配置表主键，本轮未做。

## 文档索引

- [架构与数据流](docs/architecture.md)：分层边界、两个组合根、死信分流、上报与预加载口径。
- [路由与端口快照](docs/routes-snapshot.md)：完整 HTTP 路由、请求字段、鉴权与端口映射。
- [前端重构与实施记录](docs/frontend-redesign.md)：视觉方向、路由、播放器与组件实施状态。
- 开发进度（仓库外维护，不随仓库分发）：按阶段记录提交与验收情况。
