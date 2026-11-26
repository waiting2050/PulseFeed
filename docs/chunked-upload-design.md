# 上传双通道设计：单请求 + 分片续传

本文给出素材上传「小文件走单请求、大文件走分片」的设计。实现遵循本文；文档与实现冲突时以本文的契约与校验口径为准。

## 1. 目标与非目标

目标：

- 大文件上传可断点续传：刷新、断网、进程重启后按已传分片继续，不从头再来。
- 两条通道复用同一套业务硬校验，规则一条不放松，且内容校验只跑一次。
- `POST /api/uploads` 的请求、响应、错误码、校验顺序全部保持不变。

非目标（本次不做，原因见 §10）：

- 秒传（内容 hash 命中直接复用已存在文件）。
- 图片分片上传（上限 20MB，无收益）。
- 分片级哈希（`Content-MD5`）与整文件哈希的强制校验。
- 跨实例的分布式清理协调、对象存储直传。

## 2. 现状与改动边界

| 现状 | 位置 |
| --- | --- |
| 单个 `POST /api/uploads`，multipart 提交 `file` + `kind` | `internal/interfaces/http/upload/handler.go` |
| Gin multipart 内存阈值 32MB，超出落临时文件后 `SaveUploadedFile` 再复制（写两次盘） | 同上 |
| 校验顺序：大小 → 扩展名 → ffprobe / 图片解码 | 同上 |
| 上限：视频 512MB、图片 20MB、时长 600000ms、长边 3840、编码 h264/hevc/vp9 | 同上（包级常量） |
| nginx `/api/`：`client_max_body_size 520m`、读写超时 600s | `apps/web/nginx.conf` |
| 静态回读 `GET /uploads/<kind>/<filename>` | `router.go` |
| 上传目录为 compose 命名卷 `pulsefeed-uploads:/app/uploads` | `apps/docker-compose.yml` |
| 前端一次 `FormData` + `fetch`，无进度、无重试 | `apps/web/src/lib/api.js`、`pages/PublishPage.jsx` |

改动边界：

- 不修改 `POST /api/uploads` 的任何行为，只把其中可复用的校验函数抽成内部共享函数。
- 新增 5 个 `/api/uploads/multipart*` 端点，均要求登录（与旧端点一致）。
- 新增 2 张表（仅 AutoMigrate 追加）。
- 不改 nginx、compose、config：分片大小 8MiB 远低于既有 520m 限制，路径落在既有 `/api/` location 下。

## 3. 阈值

**按文件大小判定，阈值 16MiB，分片大小 8MiB。**

推荐值与理由：

- 16MiB 以下走现有单请求：16MiB 恰好只够 2 片，往返次数的差距不足以抵消多端点带来的额外实现与失败面；这个量级在常见 4G/弱网下仍属一次请求可完成的范围。
- 16MiB 以上走分片：单请求一旦失败要重传整份（几十 MB 到 512MB），代价明显高于分片通道多出的几次往返与一次 complete。
- 分片固定 8MiB，512MB 上限对应 64 片，请求数可控；单片在弱网下的重试成本低。分片大小由服务端在 init 响应里下发，前端不自行假设，后续调整分片大小不影响老客户端续传（续传读会话里的 `chunk_size`）。
- 不用「按类型/时长」判定：图片永远走单请求（20MB 上限），视频按大小判定最直观且与业务上限解耦。

阈值只影响前端选择通道；后端两条通道始终同时可用，旧客户端不受影响。

## 4. 端点契约

统一响应信封沿用 `{code, message, data}`，错误时 `code` 等于 HTTP 状态码。所有端点需登录，只能操作自己创建的会话。

### 4.1 初始化 `POST /api/uploads/multipart`

请求体（JSON）：

```json
{ "filename": "demo.mp4", "kind": "video", "size": 134217728, "sha256": "可选，64 位小写十六进制" }
```

服务端动作（顺序固定）：

1. `kind` 白名单、扩展名白名单、`size` 上下限校验——与旧通道同一函数。`size <= 0` 直接 `400`，message 为「素材文件为空，无法上传」；`size` 超过该类别上限（视频 512MB / 图片 20MB）返回 `400`。
2. 计算 `total_parts = ceil(size / chunk_size)`，末片大小 = `size - (total_parts-1) * chunk_size`；`size` 能被 `chunk_size` 整除时末片恰好等于 `chunk_size`。
3. 先查复用：同一用户下「同 `filename` + 同 `size` + 状态 `pending`」的会话直接复用，返回其已传分片，避免刷新后重复建会话。复用命中立即返回 `200`，不再执行后续步骤。
4. 复用未命中时，再统计该用户 `status IN (pending, merging)` 的会话数，达到 3 个返回 `429`。
5. 新建会话，`upload_id` 为 32 位小写十六进制随机串，返回 `chunk_size`、`total_parts` 与空的 `uploaded_parts`。

复用条件本身的风险（同名同大小但内容已被替换时，补传会得到新旧混合文件）见 §12 待拍板 P1；本轮实现不自行增加客户端指纹。

响应 `201`（新建）/ `200`（复用）：

```json
{
  "upload_id": "8f1c...",
  "chunk_size": 8388608,
  "total_parts": 16,
  "uploaded_parts": [1, 2],
  "status": "pending"
}
```

错误：`400` 非法 kind / 扩展名 / 大小；`401` 未登录；`429` 单用户活跃会话数超过 3 个。

### 4.2 上传分片 `PUT /api/uploads/multipart/:uploadId/parts/:partNumber`

请求体为原始二进制（`application/octet-stream`），`partNumber` 从 1 开始。

校验：

- 会话存在且属于当前用户（否则 `404` / `403`）；状态必须为 `pending`。**合并开始后（`merging` 及 `completed` / `failed` / `canceled` / `expired`）PUT 一律 `409`**，不允许再写分片。
- `partNumber` 在 `1..total_parts` 内（否则 `400`）。
- 请求体大小必须等于该分片的预期大小：非末片 = `chunk_size`，末片 = `size - (total_parts-1)*chunk_size`（`size` 整除 `chunk_size` 时末片等于 `chunk_size`），否则 `400`。
- 入口用 `http.MaxBytesReader` 限制为 `chunk_size + 1` 字节。请求带 `Content-Length` 时先做一次预检，超过 `chunk_size + 1` 直接 `413`；`Transfer-Encoding: chunked` 没有该头，此时只能靠读满上限触发 `MaxBytesReader` 中断。两条路径的统一语义是**超限时不保留 `.tmp`**：已经写入的部分必须先删除，再返回 `413`（不是「不落盘」）。

落盘：先写 `<upload_id>/<partNumber>.part.tmp`，`fsync` 后 `os.Rename` 成 `.part`，保证同分片并发写或重复写只留下完整文件。

幂等：同一分片重复上传覆盖原文件并返回 `200`；不同分片可并发。

响应 `200`：

```json
{ "part_number": 3, "size": 8388608, "received_parts": 3 }
```

`received_parts` 是该会话**当前**已收的分片数，按 `COUNT(DISTINCT part_number)` 统计；同一分片重复上传只覆盖内容，不重复计数。

### 4.3 查询进度 `GET /api/uploads/multipart/:uploadId`

用于续传。响应 `200`：

```json
{
  "upload_id": "8f1c...",
  "kind": "video",
  "filename": "demo.mp4",
  "size": 134217728,
  "chunk_size": 8388608,
  "total_parts": 16,
  "uploaded_parts": [1, 2, 5],
  "status": "pending"
}
```

已完成会话额外返回 `url`、`cover_url`、`duration_ms`（与旧端点字段同名同义），便于前端在续传命中「已完成」时直接进入发布。`uploaded_parts` 按分片号升序。

**读时惰性过期**：`GET` 读到会话时，若 `updated_at` 早于 24h 且状态仍是 `pending` / `merging`，先做条件更新 `UPDATE ... SET status=expired WHERE upload_id=? AND status IN (pending, merging) AND updated_at < ?`，再返回 `410`。过期判定不依赖后台清扫是否跑过（清扫仍保留为兜底，见 §7.1）。
错误：`404` 不存在；`403` 非本人；`410` 已过期。

### 4.4 合并完成 `POST /api/uploads/multipart/:uploadId/complete`

请求体可空，或 `{ "sha256": "..." }`（init 未提供时可用它补交期望值）。

服务端动作（顺序固定）：

1. 读取会话并做惰性过期判定（同 §4.3）。随后状态 CAS：`pending → merging`。
   - CAS 成功 → 继续步骤 2。
   - CAS 失败（影响行数为 0）→ **必须重读一次会话状态再决定返回**，不能直接报错：
     - `completed` → 返回已存结果 `200`（与步骤 7 的响应同构）；
     - `merging` → `409`，`data.reason = "upload_merging"`，表示另一个 complete 正在合并，前端应轮询 `GET` 而不是判失败；
     - `failed` / `canceled` → `409`，`data.reason = "upload_terminal_state"`，message 带上终态原因；
     - `expired` → `410`。
2. 检查分片齐全；缺失返回 `409`，`data.reason = "upload_parts_incomplete"`，message 列出缺失分片号（最多 20 个，超出以「等 N 片」表述）。
3. 按序流式合并到 `<upload_id>/merged.tmp`，同时计算 SHA-256 并累计字节数；总字节数与 `size` 不符返回 `400`。
4. `sha256` 校验与回填：期望值取请求体 `sha256`，请求体未提供时取会话列的值。请求体提供了值且会话列为空时，条件回填：`UPDATE pf_upload_sessions SET sha256=? WHERE upload_id=? AND sha256 IS NULL`，然后参与本次校验。期望值存在且与实际不符 → `400`（会话置 `failed`，删除临时目录）。
5. 内容硬校验（同旧通道函数，只跑这一次）：视频走 ffprobe 校验时长/长边/编码；图片走真实格式与可解码性。
6. **落位与状态迁移绑定**：先做条件更新 `UPDATE pf_upload_sessions SET status=completed, url=?, duration_ms=? WHERE upload_id=? AND status=merging`。
   - 影响行数为 1 → 执行 `os.Rename` 到 `uploads/<kind>/<时间戳>-<随机串><ext>`；rename 失败则尝试条件回滚 `completed → failed`、删除临时目录并返回 `500`。
   - 影响行数为 0 → 会话已被取消或过期，**不执行 rename**，删除临时目录，按重读到的终态返回 `409` / `410`。
7. rename 成功后为视频抽首帧封面（ffmpeg 缺失时按既有降级只记 warning），抽帧成功则 `UPDATE pf_upload_sessions SET cover_url=? WHERE upload_id=?`（失败只记日志，按无封面返回），最后删除临时目录。

步骤 6 的 CAS 先于 rename，因此 `completed` 状态会短暂早于文件落位；但 `url` 在这一步之后才随 complete 的响应返回给客户端，外部拿不到该 URL，实际不存在可观测的 404 窗口。

响应 `201`，与旧端点**完全同构**：

```json
{ "url": "/uploads/video/20261116153000-a1b2c3d4.mp4", "kind": "video", "cover_url": "/uploads/cover/....jpg", "duration_ms": 12345 }
```

错误：`400` 分片总大小不符 / 哈希不符 / 内容非法；`403`；`404`；`409`（`data.reason` 区分 `upload_parts_incomplete` / `upload_merging` / `upload_terminal_state`）；`410` 已过期。新端点的 409 在 `data.reason` 中给出可机读原因，旧端点响应结构不变。

### 4.5 取消 `DELETE /api/uploads/multipart/:uploadId`

- `pending` / `merging` → 先 CAS 置 `canceled`，成功后再删除临时目录，`200`。
- `canceled` / `failed` → `200`（幂等）。
- `completed` → `409`（成品文件已被内容引用，禁止回删）。
- 不存在 `404`，非本人 `403`。

与合并的竞争：若合并侧已把状态推进到 `completed`（§4.4 步骤 6），取消的 CAS 影响行数为 0，此时按重读到的终态返回 `409`，不删任何文件。反向情况下取消先赢，目录已被删除，正在合并的请求在读取分片或写 `merged.tmp` 时会遇到 `ENOENT`——合并与清理路径必须把 `os.IsNotExist` 视为「会话已取消」并静默中止，不得返回 `500`、不得重试。

### 4.6 状态机

```text
                    PUT part (可乱序/重复, 仅 pending)
        ┌──────────────────────────────────┐
        │                                  ▼
      pending ──complete(分片齐, CAS)──▶ merging ──CAS + rename──▶ completed
        │                              │
        │                              ├──校验失败/合并失败──▶ failed
        │                              ├──DELETE──▶ canceled
        │                              └──读时判定 >24h──▶ expired
        ├──DELETE──▶ canceled
        └──读时判定 >24h──▶ expired
                          merging 卡住 >30min（清扫兜底）──▶ failed
```

`completed` / `failed` / `canceled` / `expired` 为终态；终态会话的 `complete` 按上表返回已存结果或 `409` / `410`。`merging → completed` 与成品 rename 由同一次条件更新绑定：更新失败的一方不落位。`merging → canceled` 与 `merging → expired` 都可能被取消/惰性过期抢占，合并侧必须以条件更新的结果为准。

## 5. 落盘与元数据

### 5.1 目录布局

```text
uploads/
  .upload-tmp/<uploadId>/000001.part ... 000064.part
  .upload-tmp/<uploadId>/merged.tmp
  video/<文件名>
  cover/<文件名>
  avatar/<文件名>
```

- 临时目录与成品目录在同一个卷（`pulsefeed-uploads`），合并完成用 `rename` 原子落位，不跨文件系统。
- `.upload-tmp` 位于静态根目录之下。**注册一个全局中间件，对路径前缀 `/uploads/.upload-tmp/` 直接 `c.AbortWithStatus(http.StatusNotFound)`**，避免半成品被公网读到；该中间件必须在 `engine.Static` 之前注册（Gin 的 `Use` 不回溯作用于已注册路由），并要有单测覆盖（至少包含正常路径与一个大小写/编码变体）。

### 5.2 元数据存 MySQL

新增两张表，仅通过 AutoMigrate 追加：

`pf_upload_sessions`

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGINT PK AUTO_INCREMENT | |
| upload_id | VARCHAR(32) UNIQUE | 对外标识 |
| user_id | BIGINT | 索引 `(user_id, status)` |
| kind | VARCHAR(16) | video/cover/avatar |
| filename | VARCHAR(255) | 仅用于取扩展名与排查 |
| size_bytes | BIGINT | 声明总大小 |
| chunk_size | INT | 建会话时固化的分片大小 |
| total_parts | INT | |
| sha256 | CHAR(64) NULL | 客户端期望值：init 提供，或 complete 补交时若为空则回填；服务端合并时计算的哈希只用于本次校验，不落库 |
| status | TINYINT | 0 pending / 1 merging / 2 completed / 3 canceled / 4 failed / 5 expired |
| url / cover_url / duration_ms | VARCHAR(512) / BIGINT | 完成结果，供幂等返回 |
| error_message | VARCHAR(255) NULL | 失败原因 |
| created_at / updated_at | DATETIME(3) | 索引 `(status, updated_at)` 供清理 |

`pf_upload_parts`

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGINT PK | |
| upload_id | VARCHAR(32) | |
| part_number | INT | 唯一索引 `(upload_id, part_number)`，让重复分片天然幂等 |
| size_bytes | INT | |
| created_at | DATETIME(3) | |

**为什么选 MySQL 而不是 Redis**：

- 上传会话生命周期是分钟到小时级，且必须跨进程重启存活。本项目的 Redis 定位是可降级缓存（`redis.addr` 为空即禁用），把续传状态放进去会出现「缓存丢 → 整个文件重传」。
- 需要按状态 + 时间做清理扫描、按用户统计活跃会话，SQL 直接表达；Redis 要额外维护多套索引。
- 项目已有统一的 AutoMigrate 清单与仓储分层，增量成本低。
- 代价是每次分片上传多一次 DB 写。分片是 8MiB 级请求，一次 INSERT/UPDATE 的开销相对网络传输可忽略。

分片文件本身是落盘事实，DB 行是索引；合并前以 DB 行为准并校验文件存在，两者不一致按缺片处理。

## 6. 校验复用

抽成共享函数（同包内私有函数，两条通道共用）：

| 校验 | 提取为 | 单请求通道时机 | 分片通道时机 |
| --- | --- | --- | --- |
| kind / 扩展名白名单 | `resolveKind(kind, filename)` | 解析 multipart 后 | init 请求内 |
| 大小上限 | `sizeLimit(kind)` + 比较 | 拿到 FileHeader 后、落盘前 | init 请求内，为零字节写入前 |
| 视频时长 / 长边 / 编码 | `validateMediaContent(ctx, path, kind, ext, limits)` | 落盘后 | 合并出 `merged.tmp` 后、rename 前 |
| 图片真实格式 | 同上（分支） | 落盘后 | 同上 |
| 首帧封面 | `enrichVideo`（复用） | 校验通过后 | 校验通过并 rename 后 |

关键点：

- 元信息校验（大小、扩展名）在**任何字节落盘之前**完成，分片通道因此不会出现「传了 500MB 才被告知扩展名不对」。
- 内容校验（ffprobe / 图片解码）在两条通道上都只跑一次，且都跑在最终产物上，口径一致。
- 分片通道的内容校验发生在 rename 之前，失败文件不会进入可公开访问的成品目录；这是相对旧通道的严格化，不改变任何对外契约。
- `ffprobe` 不可用的降级语义原样保留：记录 warning、跳过时长/分辨率/编码校验，不阻断上传。

## 7. 清理与并发

### 7.1 孤儿分片清理

- 读时惰性过期（主路径）：`GET` / `complete` 读到 `pending` / `merging` 会话时，若 `updated_at` 超过 24h，直接条件置 `expired` 并返回 `410`，不依赖清扫是否跑过（§4.3、§4.4）。
- 兜底清扫：init 请求内做一次惰性清扫，进程内最多每 10 分钟一次（`atomic` 时间戳 + `TryLock`），不新增后台 goroutine，也不影响 `cmd/api` 的启动流程。
- 清扫内容：
  1. `pending` 且 `updated_at` 早于 24h → 删临时目录，置 `expired`。
  2. `merging` 且 `updated_at` 早于 30min → 删临时目录，置 `failed`（覆盖进程在合并中崩溃的情况）。
  3. `.upload-tmp` 下 mtime 早于 24h 且无对应 `pending/merging` 会话的目录 → 直接删除（覆盖「建目录后、写 DB 前」崩溃的孤儿）。该条同时承担终态会话残留目录的回收：`failed` / `canceled` / `expired` 会话的临时目录若因异常没删干净（包括会话被置 `failed` 时第 2 步删目录失败），也由这里兜底删除——清扫不假设终态会话的目录一定已清理。
  4. 终态会话行保留 7 天后删除（先确保临时目录已删）。
- 每条清理都用带条件的 `UPDATE`/`DELETE`，多实例重复清扫不会互相破坏。

### 7.2 并发控制

- 同一 upload 并发写不同分片：互不阻塞，靠唯一索引 + 原子 rename 保证最终一致。
- 同一 upload 并发写同一分片：后到者覆盖，内容相同则为等价操作；内容不同由整文件哈希（若提供）或最终 ffprobe 兜底。
- 同一 upload 并发 complete：`UPDATE ... WHERE status=0` 的受影响行数决定唯一赢家；失败方重读状态，按 §4.4 步骤 1 的分支返回 `200` / `409(upload_merging)` / `409(upload_terminal_state)` / `410`。
- 同一用户并发上传：init 时先查复用，未命中再统计 `status IN (0,1)` 的会话数，超过 3 个返回 `429`。计数与插入非原子，极小概率多出 1 个会话，接受该偏差（副作用仅为多占一份临时空间，且被 TTL 兜底）。
- 取消与合并的竞争：取消先做 `pending/merging → canceled` 的 CAS，成功后才删目录；合并在 rename 之前做 `merging → completed` 的条件 CAS（§4.4 步骤 6），两者只有一个能成功。CAS 失败的一方不落位、不删除对方仍在使用的文件，并按重读到的终态返回。合并侧读分片或写 `merged.tmp` 遇到 `ENOENT` 时视为已被取消，静默中止并清理，不返回 `500`。

## 8. 前端方案

### 8.1 通道选择

在 `lib/api.js` 内新增 `uploadVideo(file, { onProgress, signal })`：

- `kind === 'video'` 且 `file.size > 16MiB` → 分片通道；
- 其余（含封面、头像）→ 现有 `uploadFile` 单请求通道。

`PublishPage.jsx` 的视频上传改调 `uploadVideo`，封面仍走 `uploadFile`；`FILE_KIND` 与发布契约不变。

### 8.2 进度

进度按「已成功分片字节数 / 总字节数」计算，回调 `onProgress({ loaded, total, percent, partsDone, totalParts })`。**累加必须用每个分片的实际 `size_bytes`，不能用 `partsDone * chunkSize`**：末片通常不足 8MiB，用乘法会在最后一片时超出 100% 或提前显示满进度。每片 8MiB，粒度足够；不引入 XHR 做片内字节级进度（`fetch` 无法读上传进度，改用 XHR 会与现有 401 刷新、错误信封解析逻辑分叉，收益不抵成本）。

页面展示：`正在上传素材… 42%（27/64 分片）`，沿用现有 `role="status" aria-live="polite"` 的 `pf-note`，不新增组件。

### 8.3 失败重试

- 单分片最多 3 次尝试，退避 400ms / 1200ms。
- `401`：等当前刷新结束后用新 token 重发一次（与现有 `upload()` 的语义一致，不进入重放队列）。
- `409` 按 `data.reason` 分流，不能一律判失败：
  - `upload_merging` → 说明另一个 complete 正在合并。改为每 2s 轮询一次 `GET`，最多 15 次（30s）：拿到 `completed` 就沿用返回值进入发布，拿到终态则失败并展示 message，轮询耗尽按超时提示重试。
  - `upload_parts_incomplete` → 重新 `GET` 拿到最新 `uploaded_parts`，补传缺失分片后再调 complete。
  - `upload_terminal_state` → 直接失败并展示 message。
- `404` / `410`：会话已失效，清除本地上传记录并重新 init 一次，从头传（重新 init 的复用死循环风险见 §8.4 与 §12 P2）。
- `403` / `400`（非网络类）：直接失败并展示后端 message，不重试。
- 用户可见错误文案沿用 `ApiError` 风格，不暴露端点细节。

### 8.4 刷新后续传

- 选中文件时以 `${name}:${size}:${lastModified}` 作为指纹，在 `sessionStorage` 的 `pf:upload:<fingerprint>` 下保存 `{ uploadId, kind, size, chunkSize, totalParts, uploadedParts }`（与 Feed 进度持久化同一种存储，页面级、不长期驻留）。
- 重新选到同一文件时先 `GET /api/uploads/multipart/:uploadId`：
  - `pending` → 只补传缺失分片；
  - `completed` → 直接拿 `url / cover_url / duration_ms` 进入发布；
  - `404/410` → 清理本地记录并重新 init。
- **`404/410` 的复用死循环风险**：若旧会话在服务端仍是 `pending`，重新 init 会命中复用条件（同 filename + 同 size）拿回同一个会话，形成「404/410 → init → 又是它」的循环。可选路径有两条——先 `DELETE` 旧会话再 init，或 init 带 `force_new` 跳过复用；这属于产品决策，见 §12 待拍板 P2，本轮不自行采用。
- complete 成功、用户主动放弃、或 `400/403` 终态失败时清理该键。

### 8.5 取消

V1 不新增「放弃上传」按钮；离开页面不自动取消（否则刷新后续传失效），依赖服务端 24h TTL 回收。`cancelMultipartUpload` 仅作为 api 层能力保留，供后续显式放弃入口使用。

## 9. 与现有单请求通道的兼容性

- `POST /api/uploads` 的路径、multipart 字段（`file`、`kind`）、状态码（`201`、`400`、`413`、`500`）、响应字段（`url`、`kind`、`cover_url`、`duration_ms`）、校验顺序与错误文案均不变。
- 旧客户端（含未更新的前端、脚本、CI 用例）行为完全不变；新端点只在 `/api/uploads/multipart*` 下增量出现。
- 两条通道产出的 `url` 形态一致（`/uploads/<kind>/<filename>`），`publishClip` 无需区分来源。
- 新增端点同样要求登录，不引入匿名写入口；静态回读规则不变，仅增加 `.upload-tmp` 的 404 拦截。
- 数据库只新增表，不改动任何既有表结构；`migration.Models()` 追加两项。

## 10. 已知风险与不做

**风险**

1. **本地盘/共享盘约束**：分片与成品都写在 `pulsefeed-uploads` 卷上。API 多副本时必须是共享卷，否则 `GET`、分片 `PUT`、`complete` 可能落到不同副本而找不到文件。当前 compose 单副本，满足；横向扩容前需要换成共享存储或对象存储，文档在此记录。
2. **合并是同步耗时操作**：512MB 合并 + ffprobe 在慢盘上可能数十秒，占用一个请求。nginx 600s 超时覆盖，但客户端需要容忍长请求；前端在 `complete` 阶段展示「正在校验素材…」以免误判卡死。
3. **无分片哈希**：单分片在传输中被破坏且客户端未提供整文件 sha256 时，只能靠最终 ffprobe/解码发现部分损坏，不能发现「能解码但内容错」的情况。这是本轮接受的缺口。
4. **会话数上限存在轻微竞态**：极端并发下可能多建 1 个会话，由 TTL 兜底。
5. **`.upload-tmp` 拦截依赖中间件注册位置**：全局中间件必须在 `engine.Static` 之前注册（Gin 的 `Use` 不回溯作用于已注册路由），改动路由顺序时需保留该约束，并有单测锁定。
6. **清理依赖有上传行为**：没有新上传时不会触发兜底清扫，磁盘占用可能延迟回收；读时惰性过期已覆盖 `GET` / `complete` 路径，其余孤儿目录由清扫兜底。

**不做**

- **秒传**：需要客户端整文件哈希。浏览器侧 `crypto.subtle.digest` 必须一次性读入整个文件（512MB 在移动端不可接受），引入流式哈希库则要新增前端依赖，违反现有「不新增依赖」约定；同时跨用户内容复用会带来「一方删除导致另一方文件失效」的引用计数问题。本轮服务端合并时计算的 SHA-256 只用于本次完整性校验、不落库；`pf_upload_sessions.sha256` 始终只存客户端期望值（init 提供，或 complete 补交时为空则回填），为后续秒传保留语义一致的字段。
- 图片分片、分片级哈希、断点续传的服务端主动推送、多副本分布式清扫、对象存储直传。

## 11. 验收与测试计划

单元/接口用例（`go test ./...` 内，`t.TempDir()` + 假 prober + 内存仓储）：

1. 正常分片上传：init → 乱序 PUT 全部分片 → complete，产物落盘且响应字段与旧端点一致。
2. 分片乱序：先传末片再传首片，合并结果字节序正确。
3. 重复传同一分片：返回 200，`received_parts` 不重复计数，最终文件正确。
4. 断点续传：新建服务实例（新 handler + 同一仓储）后 `GET` 拿到已传分片并只补传缺失片。
5. 合并校验失败：声明 `sha256` 与实际不符 → 400、状态 failed、临时目录被删；ffprobe 判定超时长/超长边/非法编码 → 400 且成品目录无文件。
6. complete 幂等：首次 complete 成功（`201`）后，再次提交同一 `uploadId` 的 complete 返回 `200` 与**完全相同的响应体**（同一 `url`、`cover_url`、`duration_ms`），且不重复执行 rename、不重复跑 ffprobe / 抽帧（用假 prober 的调用计数断言只调用一次）。与相邻用例的分工：用例 5 覆盖「`sha256` 不符 → 400 且会话置 `failed`」的失败路径；用例 11 覆盖 CAS 竞态下的 `409` 分流，其中 `completed` 分支的 200 行为由本条断言，不再重复。原「已完成返回 200」并入本条，作为幂等的主断言。
7. 超限与非法扩展名：init 即 400，且不产生临时目录。
8. 分片超长/过短、越界分片号、非本人会话、已取消会话 → 对应 4xx。
9. 孤儿清理：伪造过期 `pending` 会话与无会话临时目录，触发清扫后目录消失、状态正确；另造一个 `failed` 会话且其临时目录仍存在，验证同一条清扫能删除该残留目录。
10. 读时惰性过期：把 `pending` / `merging` 会话的 `updated_at` 改到 24h 之前，`GET` / `complete` 直接返回 `410` 且状态变 `expired`（不触发清扫）。
11. CAS 竞态分流：complete 遇到 `merging` 返回 `409 + data.reason=upload_merging`，遇到缺片返回 `409 + upload_parts_incomplete`，两者可区分；遇到 `completed` 时走用例 6 的幂等分支，这里只断言它不会被误判为 `409`。
12. 取消与合并竞争：取消先赢时，合并侧遇到 `ENOENT` 静默中止、不返回 `500`；合并侧先完成时，取消返回 `409` 且成品文件保留。
13. `.upload-tmp` 静态访问返回 `404`（全局中间件拦截）。
14. 旧通道回归：现有 `handler_test.go` / `upload_test.go` / `upload_static_range_test.go` 全部保持通过。

集成用例（build tag 隔离，连本机 MySQL/Redis）：

- 用真实 MySQL 跑 init → 分片 → complete 全链路，校验两张表的行与状态流转；
- **集成测试使用独立 DSN / 独立 schema**，不得在共享库或开发库上执行 AutoMigrate，也不得触碰既有演示数据（clip 15/16/17 及其评论）；跑完删除该 schema 或清表；
- 若本机 MySQL/Redis 不可用，如实说明未执行，不伪造结果。

回归命令：`go build ./...`、`go vet ./...`、`go test ./...`、`npm run build`、变更 Go 文件按 LF 导出后 `gofmt -l` 无输出、禁用字样检索。

## 12. 待拍板

以下两条属于产品决策，文档只记录风险与可选路径，本轮实现不自行采用其中任何一种。

**P1. 会话复用是否引入客户端指纹**

- 现状设计：同 `filename` + 同 `size` + `pending` 即复用（§4.1）。
- 风险：用户修改了同名同大小文件的少量内容后重新上传，复用时只补传缺失分片，最终会得到新旧内容混合的文件；若客户端未提供整文件 `sha256`，服务端只能靠 ffprobe / 图片解码兜住「文件损坏」，无法发现「能解码但内容混合」。
- 可选路径：
  1. 维持现状，靠可选 `sha256` 兜底；用户不提供时风险由客户端承担。
  2. init 增加客户端指纹字段（`lastModified` 或 `sha256`），指纹不一致一律新建会话，不做复用。
- 需要拍板：选 1 还是 2；若选 2，指纹取什么（`lastModified` 轻量但可伪造，`sha256` 准确但计算成本高）。

**P2. `404/410` 后重新初始化的路径**

- 现状设计：`404/410` → 清理本地记录并重新 init（§8.4）。
- 风险：旧会话在服务端仍为 `pending` 时，重新 init 会命中复用条件拿回同一会话，形成死循环。
- 可选路径：
  1. 先 `DELETE` 旧会话再 init：多一次请求，语义最直白，不需要新增字段。
  2. init 请求带 `force_new: true` 跳过复用：少一次往返，但给客户端新增一个可选字段。
- 需要拍板：选 1 还是 2。

## 13. 修订记录

本文档经两轮评审修订，**仅改动本文档**，未写实现、未改其他文件、未提交 Git。

**第一轮修订**

| # | 评审意见 | 落到 | 处理 |
| --- | --- | --- | --- |
| 1 | complete CAS 竞态需重读状态 | §4.4、§8.3、§7.2、§11 | **直接定稿**：CAS 失败重读；`completed` → 200，`merging` → 409 且 `data.reason=upload_merging`（与缺片 `upload_parts_incomplete`、终态 `upload_terminal_state` 区分）；前端收到 merging 的 409 改为轮询 GET |
| 2 | 取消与合并的竞争窗口 | §4.4、§4.5、§4.6、§7.2 | **直接定稿**：rename 前做 `merging → completed` 条件更新，0 行则不 rename、清临时目录并按终态返回；状态机补 `merging → canceled` 与 `merging → expired` 出边 |
| 3 | 410 改为读时惰性判定 | §4.3、§4.4、§4.6、§7.1 | **直接定稿**：GET / complete 读到 `pending` / `merging` 且超 24h 即条件置 `expired` 并返回 410；清扫保留为兜底 |
| 4 | MaxBytesReader 措辞与 Content-Length | §4.2 | **直接定稿**：改为「超限时不保留 `.tmp`」，删除已写部分再 413；有 `Content-Length` 时预检，chunked 无该头只能靠读满中断 |
| 5 | 末片大小公式与 `size=0` | §4.1、§4.2 | 补：`total_parts=ceil(size/chunk_size)`、整除时末片=chunk_size、`size<=0` 返回 400「素材文件为空，无法上传」 |
| 6 | 复用条件的误复用风险 | §4.1、§12 P1 | **待拍板**：只记录风险与两条路径，不自行采用 |
| 7 | sessionStorage 404/410 死循环 | §8.4、§12 P2 | **待拍板**：只记录风险与两条路径，不自行采用 |
| 8 | init 顺序 | §4.1 | **直接定稿**：先查复用并即时返回，未命中再统计活跃会话，≥3 返回 429 |
| 9 | `sha256` 语义统一 | §4.4、§5.2、§10 | **直接定稿**：列只存客户端期望值，complete 可回填；服务端计算结果只用于校验、不落库 |
| 10 | DELETE 与 merging 的容错 | §4.5、§7.2、§11 | 补：合并/清理路径遇 `ENOENT` 视为已取消，静默中止，不返回 500 |
| 11 | `.upload-tmp` 拦截表述 | §5.1、§10 | **直接定稿**：注册全局中间件，对 `/uploads/.upload-tmp/` 前缀 `AbortWithStatus(404)`；保留单测要求 |
| 12 | `received_parts` 语义 | §4.2 | 补：`COUNT(DISTINCT part_number)`，重复上传同一片不重复计数 |
| 13 | 集成测试隔离 | §11 | 补：独立 DSN / 独立 schema，不得在共享库或开发库执行 AutoMigrate |
| 14 | 非阻塞措辞 | §3、§4.2、§8.2 | 改：16MiB 恰好 2 片、往返差距不足以抵消多端点失败面；补「合并开始后 PUT 一律 409」；进度按实际 `size_bytes` 累加，禁用 `partsDone * chunkSize` |

**第二轮修订（本次）**

| # | 评审意见 | 落到 | 处理 |
| --- | --- | --- | --- |
| 15 | 兜底清扫第 3 条的覆盖面 | §7.1 | 补：该条同时回收 `failed` / `canceled` / `expired` 会话的残留目录，包括「会话被置 `failed` 时第 2 步删目录失败」的情况；清扫不假设终态会话目录已清理干净 |
| 16 | complete 幂等需单独成条 | §11 | 新增用例 6：首次成功后再 complete 返回 200 与同一响应体，不重复 rename、不重复跑探测/抽帧；原「已完成返回 200」并入本条，用例 11 只保留 409 分流断言，并写清与用例 5（哈希失败）的分工 |
| 17 | §8.2 进度口径确认 | §8.2 | 无需改动：`partsDone` 显示整数分片数、百分比按实际 `size_bytes` 累加，该表述已是正确口径，此处留档确认 |
