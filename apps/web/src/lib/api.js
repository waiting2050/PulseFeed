// api.js 收口所有 HTTP 访问：统一信封解析、Bearer 注入、幂等键、上传与错误信息。
const TOKEN_KEY = 'pulsefeed.token'

// 上传接口沿用的既有 kind 取值与创建接口字段名，不在前端另起别名。
const FILE_KIND = 'video'

let accessToken = ''

export class ApiError extends Error {
  constructor(message, code = 0) {
    super(message)
    this.name = 'ApiError'
    this.code = code
  }
}

// hydrateToken 从本地存储恢复登录态，应用启动时调用一次。
export function hydrateToken() {
  accessToken = localStorage.getItem(TOKEN_KEY) || ''
  return accessToken
}

export function getToken() {
  return accessToken
}

// setToken 同步内存与本地存储；传空值即清除登录态。
export function setToken(token) {
  accessToken = token || ''
  if (accessToken) {
    localStorage.setItem(TOKEN_KEY, accessToken)
  } else {
    localStorage.removeItem(TOKEN_KEY)
  }
}

function newIdempotencyKey() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return `pf-${Date.now()}-${Math.random().toString(16).slice(2)}`
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

async function request(path, { method = 'GET', body, headers = {} } = {}) {
  const finalHeaders = { ...headers }
  if (body !== undefined) finalHeaders['Content-Type'] = 'application/json'
  if (accessToken) finalHeaders.Authorization = `Bearer ${accessToken}`
  if (method !== 'GET' && method !== 'HEAD') {
    finalHeaders['Idempotency-Key'] = newIdempotencyKey()
  }

  let response
  try {
    response = await fetch(`/api${path}`, {
      method,
      headers: finalHeaders,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError('网络异常，请检查连接后重试')
  }
  return readEnvelope(response)
}

async function upload(path, formData) {
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
    request('/users', { method: 'POST', body: { account, password, nickname: nickname || undefined } }),

  login: ({ account, password }) =>
    request('/sessions', { method: 'POST', body: { account, password } }),

  logout: () => request('/sessions/current', { method: 'DELETE' }),

  me: () => request('/users/me'),

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

  // 沿用既有发布契约：先上传素材，再提交 title / video_url / cover_url；duration_ms 由上传探测回传。
  publishClip: ({ title, fileUrl, coverUrl, durationMs }) =>
    request('/clips', {
      method: 'POST',
      body: {
        title,
        video_url: fileUrl,
        cover_url: coverUrl || '',
        duration_ms: Number(durationMs) || 0,
      },
    }),

  messages: ({ limit = 20, cursor = '' } = {}) =>
    request(`/messages${buildQuery({ limit, cursor })}`),

  markAllRead: () => request('/messages', { method: 'PATCH' }),

  playbackConfig: () => request('/playback-config'),

  // 复用既有 QoS 契约：event_id 由客户端生成，后端按其幂等，同一事件重复提交不会重复落库。
  reportQoS: (report) => request('/playback-qos-reports', { method: 'POST', body: report }),

  savePlaybackConfig: (config) => request('/playback-config', { method: 'PUT', body: config }),

  preloadClips: () => request('/preload-clips'),
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
