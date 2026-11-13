// api.js 收口所有 HTTP 访问：统一信封解析、Bearer 注入、幂等键、401 单飞刷新与挂起重放。
const ACCESS_KEY = 'pulsefeed.token'
const REFRESH_KEY = 'pulsefeed.refresh'

// 上传接口沿用的既有 kind 取值与创建接口字段名，不在前端另起别名。
const FILE_KIND = 'video'

// 429 退避重试的固定间隔；只重试一次，避免限流时形成自激。
const RATE_LIMIT_BACKOFF_MS = 1000

let accessToken = ''
let refreshing = null // 模块级单飞 Promise：并发 401 只发起一次刷新
let pending = [] // 刷新期间挂起的可重放请求
let listeners = [] // App.jsx 订阅登录态变化

export class ApiError extends Error {
  constructor(message, code = 0) {
    super(message)
    this.name = 'ApiError'
    this.code = code
  }
}

// hydrateToken 从本地存储恢复登录态，应用启动时调用一次。
export function hydrateToken() {
  accessToken = localStorage.getItem(ACCESS_KEY) || ''
  return accessToken
}

export function getToken() {
  return accessToken
}

// getRefreshToken 每次现读 localStorage，避免多标签页拿到已被轮换的旧值。
export function getRefreshToken() {
  return localStorage.getItem(REFRESH_KEY) || ''
}

// subscribeSession 让 App.jsx 订阅登录态变化；返回取消订阅函数。
export function subscribeSession(listener) {
  listeners.push(listener)
  return () => {
    listeners = listeners.filter((item) => item !== listener)
  }
}

function emitSession(token) {
  accessToken = token || ''
  listeners.forEach((listener) => listener(accessToken))
}

// applyPair 写入一对令牌并通知订阅者；登录与刷新共用。
function applyPair(data) {
  const access = (data && data.access_token) || ''
  localStorage.setItem(ACCESS_KEY, access)
  if (data && data.refresh_token) localStorage.setItem(REFRESH_KEY, data.refresh_token)
  emitSession(access)
}

// clearSession 清空两个令牌并通知订阅者；刷新失败与主动登出共用。
export function clearSession() {
  localStorage.removeItem(ACCESS_KEY)
  localStorage.removeItem(REFRESH_KEY)
  emitSession('')
}

function forceLogin() {
  if (typeof window === 'undefined') return
  const next = encodeURIComponent(window.location.pathname + window.location.search)
  if (!window.location.pathname.startsWith('/login')) {
    window.location.replace(`/login?next=${next}`)
  }
}

// newClientId 生成客户端事件/批次标识；优先使用 crypto.randomUUID，缺失时回退时间戳随机串。
export function newClientId(prefix = 'pf') {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return `${prefix}-${globalThis.crypto.randomUUID()}`
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function newIdempotencyKey() {
  return newClientId('pf')
}

async function readEnvelope(response) {
  let payload = null
  try {
    payload = await response.json()
  } catch {
    payload = null
  }
  if (!response.ok || (payload && payload.code !== 0)) {
    const message =
      (payload && payload.message) ||
      (response.status === 401 ? '登录状态已失效，请重新登录' : `请求失败 (${response.status})`)
    throw new ApiError(message, (payload && payload.code) || response.status)
  }
  return payload ? payload.data : null
}

// isAuthError 只认 access token 的两种失败；40103 与登录失败的 401 不触发刷新。
function isAuthError(err) {
  return err instanceof ApiError && (err.code === 40101 || err.code === 40102)
}

// 写请求重放白名单：只有重复执行无副作用的接口才允许刷新后自动重放。
// 非 GET 默认不重放；新增写接口必须显式登记，漏登记只会退化为“用户手动重试”。
const REPLAYABLE_WRITES = [
  /^POST \/clips$/,
  /^POST \/clips\/\d+\/comments$/,
  /^(PUT|DELETE) \/clips\/\d+\/(like|save)$/,
  /^(PUT|DELETE) \/users\/me\/following\/\d+$/,
  /^PATCH \/messages$/,
  /^PATCH \/users\/me$/,
  /^DELETE \/clips\/\d+$/,
  /^DELETE \/comments\/\d+$/,
  /^DELETE \/sessions\/current$/,
]

function isReplayable(method, path) {
  if (method === 'GET' || method === 'HEAD') return true
  return REPLAYABLE_WRITES.some((pattern) => pattern.test(`${method} ${path}`))
}

async function send(path, opts = {}, token = accessToken) {
  const method = opts.method || 'GET'
  const headers = { ...(opts.headers || {}) }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (token) headers.Authorization = `Bearer ${token}`
  if (method !== 'GET' && method !== 'HEAD') {
    headers['Idempotency-Key'] = newIdempotencyKey()
  }

  let response
  try {
    response = await fetch(`/api${path}`, {
      method,
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal,
    })
  } catch {
    throw new ApiError('网络异常，请检查连接后重试')
  }
  return readEnvelope(response)
}

// upload 不进入重放队列：已经收到 401 的上传按失败抛给页面；
// 若刷新正在进行，则等刷新结束后用新 access token 发送一次（属于首次发送，不是重放）。
async function upload(path, formData) {
  if (refreshing) {
    await refreshing.catch(() => {})
  }
  const headers = { 'Idempotency-Key': newIdempotencyKey() }
  if (accessToken) headers.Authorization = `Bearer ${accessToken}`

  let response
  try {
    response = await fetch(`/api${path}`, { method: 'POST', headers, body: formData })
  } catch {
    throw new ApiError('网络异常，上传失败，请重试')
  }
  return readEnvelope(response)
}

async function request(path, opts = {}) {
  if (opts.skipAuth) return send(path, opts, '')
  if (!accessToken) return send(path, opts, '') // 未登录：不带 token，也不刷新

  const method = opts.method || 'GET'

  if (refreshing) {
    if (opts.ephemeral) return send(path, opts, accessToken) // 即发即忘：不等待、不排队
    if (!isReplayable(method, path)) {
      await refreshing // 刷新失败则后续 send 会以未登录身份失败
      return send(path, opts, accessToken)
    }
    return enqueue(path, opts)
  }

  try {
    return await send(path, opts, accessToken)
  } catch (err) {
    if (!isAuthError(err)) throw err
    if (opts.ephemeral) throw err // 即发即忘：直接丢弃
    if (!getRefreshToken()) {
      clearSession()
      forceLogin()
      throw err
    }
    if (!isReplayable(method, path)) {
      await ensureRefresh().catch(() => {}) // 只恢复会话，不重放本次请求
      throw err
    }
    return enqueue(path, opts)
  }
}

function enqueue(path, opts) {
  return new Promise((resolve, reject) => {
    pending.push({ path, opts, resolve, reject })
    ensureRefresh()
  })
}

// performRefresh 调用刷新接口；429 时保留令牌、退避 1 秒重试一次。
async function performRefresh() {
  const refreshToken = getRefreshToken()
  if (!refreshToken) throw new ApiError('登录状态已失效，请重新登录', 40103)
  const body = { refresh_token: refreshToken }
  try {
    return await send('/sessions/refresh', { method: 'POST', body }, '')
  } catch (err) {
    if (err instanceof ApiError && err.code === 429) {
      await new Promise((resolve) => setTimeout(resolve, RATE_LIMIT_BACKOFF_MS))
      return send('/sessions/refresh', { method: 'POST', body: { refresh_token: getRefreshToken() } }, '')
    }
    throw err
  }
}

function ensureRefresh() {
  if (refreshing) return refreshing
  refreshing = performRefresh()
    .then((data) => {
      applyPair(data)
      return replayPending()
    })
    .catch((err) => {
      const queued = pending
      pending = []
      if (err instanceof ApiError && err.code === 429) {
        // 限流：保留本地令牌与用户态，只把错误交回给挂起项。
        queued.forEach((item) => item.reject(err))
        return
      }
      clearSession()
      queued.forEach((item) => item.reject(err))
      forceLogin()
    })
    .finally(() => {
      refreshing = null
    })
  return refreshing
}

async function replayPending() {
  const queued = pending
  pending = []
  for (const item of queued) {
    try {
      item.resolve(await send(item.path, item.opts, accessToken))
    } catch (err) {
      if (isAuthError(err)) {
        // 重放仍 401：放弃整体会话，剩余请求一并失败。
        clearSession()
        queued.forEach((rest) => rest.reject(err))
        forceLogin()
        return
      }
      item.reject(err) // 业务错误按原样返回
    }
  }
}

function buildQuery(params) {
  const search = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') search.set(key, String(value))
  })
  const text = search.toString()
  return text ? `?${text}` : ''
}

export const api = {
  register: ({ account, password, nickname }) =>
    request('/users', {
      method: 'POST',
      body: { account, password, nickname: nickname || undefined },
      skipAuth: true,
    }),

  // 登录成功后由 applyPair 统一写入两个键，调用方只消费返回的资料字段。
  login: async ({ account, password }) => {
    const data = await request('/sessions', {
      method: 'POST',
      body: { account, password },
      skipAuth: true,
    })
    applyPair(data)
    return data
  },

  logout: () => request('/sessions/current', { method: 'DELETE' }),

  me: () => request('/users/me'),

  // 公开主页：资料、作品与关系概览；列表接口均复用游标分页契约。
  getUser: (userId) => request(`/users/${userId}`),

  updateMe: (patch) => request('/users/me', { method: 'PATCH', body: patch }),

  userClips: (userId, { limit = 10, cursor = '' } = {}) =>
    request(`/users/${userId}/clips${buildQuery({ limit, cursor })}`),

  myClips: ({ limit = 10, cursor = '' } = {}) =>
    request(`/users/me/clips${buildQuery({ limit, cursor })}`),

  relationStats: (userId) => request(`/users/${userId}/relation`),

  follow: (userId, active) =>
    request(`/users/me/following/${userId}`, { method: active ? 'PUT' : 'DELETE' }),

  userFollowing: (userId, { limit = 20, cursor = '' } = {}) =>
    request(`/users/${userId}/following${buildQuery({ limit, cursor })}`),

  userFollowers: (userId, { limit = 20, cursor = '' } = {}) =>
    request(`/users/${userId}/followers${buildQuery({ limit, cursor })}`),

  unread: () => request('/message-stats/unread'),

  feed: ({ scene, cursor = '', limit = 10 }) => request(`/feed${buildQuery({ scene, cursor, limit })}`),

  // 单条内容查询支撑 /clip/:id 深链，列表里没有该条时用它补齐详情。
  getClip: (clipId) => request(`/clips/${clipId}`),

  comments: (clipId, { limit = 20, cursor = '' } = {}) =>
    request(`/clips/${clipId}/comments${buildQuery({ limit, cursor })}`),

  createComment: (clipId, content) =>
    request(`/clips/${clipId}/comments`, { method: 'POST', body: { content } }),

  like: (clipId, active) => request(`/clips/${clipId}/like`, { method: active ? 'PUT' : 'DELETE' }),

  save: (clipId, active) => request(`/clips/${clipId}/save`, { method: active ? 'PUT' : 'DELETE' }),

  uploadFile: (file, kind) => {
    const form = new FormData()
    form.append('file', file)
    form.append('kind', kind)
    return upload('/uploads', form)
  },

  // 沿用既有发布契约：先上传素材，再提交 title / description / video_url / cover_url；
  // description 可选，最长 1000 字符；duration_ms 由上传探测回传。
  publishClip: ({ title, description, fileUrl, coverUrl, durationMs }) =>
    request('/clips', {
      method: 'POST',
      body: {
        title,
        description: description || '',
        video_url: fileUrl,
        cover_url: coverUrl || '',
        duration_ms: Number(durationMs) || 0,
      },
    }),

  messages: ({ limit = 20, cursor = '' } = {}) =>
    request(`/messages${buildQuery({ limit, cursor })}`),

  // 按条已读：ids 非空时只标记当前用户名下的这些消息；不传 ids 即全部已读。
  markRead: (ids) => request('/messages', { method: 'PATCH', body: { ids } }),

  markAllRead: () => request('/messages', { method: 'PATCH' }),

  playbackConfig: () => request('/playback-config'),

  // 复用既有 QoS 契约：event_id 由客户端生成，后端按其幂等，同一事件重复提交不会重复落库。
  reportQoS: (report) =>
    request('/playback-qos-reports', { method: 'POST', body: report, ephemeral: true }),

  // 展示/播放事件上报：event_type 区分 exposed/play/complete/skip，request_id 关联同一批 Feed 请求。
  reportPlayEvent: ({ clipId, eventId, eventType, scene, requestId, watchMs = 0, completed = false }) =>
    request('/clip-play-events', {
      method: 'POST',
      ephemeral: true,
      body: {
        clip_id: clipId,
        event_id: eventId,
        event_type: eventType,
        scene,
        request_id: requestId,
        watch_ms: Math.max(0, Math.round(watchMs)),
        completed,
      },
    }),

  savePlaybackConfig: (config) => request('/playback-config', { method: 'PUT', body: config }),

  preloadClips: ({ signal } = {}) => request('/preload-clips', { signal, ephemeral: true }),
}

// 多标签页：access 变化时同步内存令牌；refresh 被另一标签页清除时视为会话失效。
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key === ACCESS_KEY) {
      emitSession(event.newValue || '')
    }
    if (event.key === REFRESH_KEY && !event.newValue) {
      clearSession()
      forceLogin()
    }
  })
}

// 播放配置在登录后拉取一次并缓存；播放结算与配置页共用，避免每次播放都请求。
let cachedPlaybackConfig = null
let playbackConfigRequest = null

export function getCachedPlaybackConfig() {
  return cachedPlaybackConfig
}

export function primePlaybackConfig(config) {
  if (!config) return
  cachedPlaybackConfig = { ...config }
  playbackConfigRequest = null
}

export function invalidatePlaybackConfig() {
  cachedPlaybackConfig = null
  playbackConfigRequest = null
}

export function loadPlaybackConfig() {
  if (cachedPlaybackConfig) return Promise.resolve(cachedPlaybackConfig)
  if (!playbackConfigRequest) {
    playbackConfigRequest = api
      .playbackConfig()
      .then((data) => {
        cachedPlaybackConfig = data
        playbackConfigRequest = null
        return data
      })
      .catch((err) => {
        playbackConfigRequest = null
        throw err
      })
  }
  return playbackConfigRequest
}

export { FILE_KIND }
