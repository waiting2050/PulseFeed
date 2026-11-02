# PulseFeed

PulseFeed 是一个原创复刻的 Feed 系统：Go + Gin 后端提供账号、发布、Feed、互动、关注、推荐、热榜、消息与播放配置；React + Vite 前端提供最小可用的登录与 Feed 浏览闭环。

## 项目定位

- 后端按领域分层组织：`domain` / `application` / `infra` / `interfaces`，接口统一返回 `{ code, message, data }` 信封。
- 数据面使用 MySQL 持久化、Redis 做缓存与热度分钟桶、RabbitMQ 做异步事件（发布扇出、互动落库、向量生成、播放事件补偿）。
- 前端为 S18 最小闭环：登录/注册、latest Feed 首屏、`next_cursor` 驱动的加载更多（按 `clip_id` 去重）。

## 目录结构

```
pulsefeed/
├── apps/
│   ├── api/                     # Go module（module: pulsefeed，Go 1.22）
│   │   ├── cmd/
│   │   │   ├── api/             # HTTP 入口（默认 8090）
│   │   │   └── worker/          # 异步消费者入口（-metrics-addr 可覆盖指标端口）
│   │   ├── configs/             # config.yaml（goccy/go-yaml，缺失即报错）
│   │   ├── internal/
│   │   │   ├── domain/          # 领域模型与仓储端口
│   │   │   ├── application/     # 用例编排（feed/recommendation/impression/playback/...）
│   │   │   ├── infra/           # MySQL/Redis/RabbitMQ/指标等实现
│   │   │   └── interfaces/http/ # handler、dto、路由与中间件
│   │   └── test/                # 基于内存 fake 的接口测试
│   └── web/                     # React + Vite 前端
│       ├── index.html
│       ├── vite.config.js       # /api 与 /uploads 代理到 127.0.0.1:8090
│       └── src/{main.jsx,App.jsx,styles.css}
└── README.md
```

## 启动方式

前置依赖：MySQL（3306）、Redis（6379）、RabbitMQ（5672）。连接参数见 `apps/api/configs/config.yaml`，首次启动自动执行 GORM 迁移。

```bash
# 1) 后端 API
cd pulsefeed/apps/api
go run ./cmd/api

# 2) 异步 Worker（本机 9092 可能落在系统保留段，可用参数覆盖）
cd pulsefeed/apps/api
go run ./cmd/worker -metrics-addr :19092

# 3) 前端
cd pulsefeed/apps/web
npm install
npm run dev          # 默认 http://127.0.0.1:5173，/api 与 /uploads 代理到 8090
```

健康检查与指标：`GET http://127.0.0.1:8090/health`、`GET http://127.0.0.1:8090/metrics`、Worker 指标 `GET http://127.0.0.1:19092/metrics`。

## 容器化启动（S20，待人工确认）

前置：Docker Desktop 已启动，且 8088/8090/19092/3307/6380/5673 未被占用。

```bash
cd pulsefeed/apps
docker compose up -d --build     # 首次会构建 api/worker/web 镜像
curl http://127.0.0.1:8090/health
# 浏览器打开 http://127.0.0.1:8088
docker compose stop             # 停止容器，保留数据卷
```

| 服务 | 宿主机端口 | 容器端口 | 说明 |
|---|---|---|---|
| api | 8090 | 8090 | HTTP API 与 `/health`、`/metrics` |
| worker | 19092 | 9092 | 指标端口固定映射，避开 Hyper-V 动态保留段 |
| web | 8088 | 80 | nginx 托管静态产物，反代 `/api`、`/uploads` |
| mysql | 3307 | 3306 | 避免与本机 3306 冲突 |
| redis | 6380 | 6379 | |
| rabbitmq | 5673 / 15673 | 5672 / 15672 | 15673 为管理台 |
| prometheus | 9090 | 9090 | 仅 `--profile monitoring`，配置属 S21 |
| grafana | 3000 | 3000 | 仅 `--profile monitoring`，配置属 S21 |

容器内配置为 `apps/api/configs/config.docker.yaml`（`mysql`/`redis`/`rabbitmq` 为服务名），连接参数与 compose 环境变量一致。

## 阶段进度

当前阶段与历史验收记录见仓库根目录的《PulseFeed-仿写开发进度文档.md》：[../PulseFeed-仿写开发进度文档.md](../PulseFeed-仿写开发进度文档.md)。

- 已完成并登记：S1–S18（S13 播放事件与曝光、S14 推荐与向量、S15 hot 热榜、S16 消息/播放配置/QoS、S17 可观测性与依赖收口、S18 前端最小闭环）。
- 当前：S20 容器化与本地编排（待人工确认）。
- 后续排期：S20 之后为 S21（监控 provision 与 K8s 清单）；S19 前端功能补齐暂缓。Worker metrics 在 compose 中固定映射宿主机 `19092`，不依赖默认 `:9092`。

## 测试探针数据

数据库中保留了用于 `pf_clip_stats.play_count` 回填与幂等校验的测试探针行（clip `21`、`22`、`30`）及对应计数。它们用于核对「播放开始」口径与重复上报不重复累计，请勿直接清库；S20 使用全新库时自然规避。
