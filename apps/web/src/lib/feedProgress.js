// feedProgress.js 把 Feed 各场景的时间线进度（已加载列表、游标、是否还有下一页）持久化到
// sessionStorage，让关页、刷新、从详情返回时接着上次的位置继续，而不是重新拉第一页。
// 每个用户和场景独立分键；结构版本写在数据内，版本不符、解析失败或数据形状不对时直接丢弃并清理，
// 调用方据此回落到第一页，避免把用户卡在坏数据上。

// FEED_PROGRESS_VERSION 是持久化结构版本：字段或语义变化时递增，旧数据自动失效。
const FEED_PROGRESS_VERSION = 2
const KEY_PREFIX = 'pulsefeed.feed.v2.'

// JWT subject 仅作本地缓存分区，不用于鉴权；无有效 subject 时禁用缓存。
export function feedCacheOwner(token) {
  try {
    const part = token.split('.')[1]
    const payload = JSON.parse(atob(part.replace(/-/g, '+').replace(/_/g, '/')))
    return typeof payload.sub === 'string' && /^[1-9]\d*$/.test(payload.sub) ? payload.sub : ''
  } catch {
    return ''
  }
}

// 按用户及场景隔离，旧版 v1 无用户标识的记录不再读。
function storageKey(owner, scene) {
  if (typeof owner !== 'string' || !/^[1-9]\d*$/.test(owner) || !scene) return null
  return `${KEY_PREFIX}${owner}.${scene}`
}

// getStorage 取 sessionStorage；不支持或访问被拒时返回 null，进度持久化整体降级为不缓存。
function getStorage() {
  if (typeof window === 'undefined') return null
  try {
    return window.sessionStorage || null
  } catch {
    return null
  }
}

// clearFeedProgress 删除某个场景的进度；清理失败静默，不影响列表正确性。
export function clearFeedProgress(owner, scene) {
  const storage = getStorage()
  const key = storageKey(owner, scene)
  if (!storage || !key) return
  try {
    storage.removeItem(key)
  } catch {
    // 存储不可写时无需处理：下次恢复读不到数据会自然回到第一页。
  }
}

// 登出时仅删除本应用的 Feed 缓存，不清空其他 sessionStorage 数据。
export function clearAllFeedProgress() {
  const storage = getStorage()
  if (!storage) return
  try {
    for (let i = storage.length - 1; i >= 0; i--) {
      const key = storage.key(i)
      if (key && (key.startsWith(KEY_PREFIX) || key.startsWith('pulsefeed.feed.v1.'))) {
        storage.removeItem(key)
      }
    }
  } catch {
    // 缓存不可访问时不影响登录态清理。
  }
}

// sanitizeItems 过滤掉缺 clip_id 的脏条目并规范化 ID 类型。
function sanitizeItems(items) {
  return items
    .filter((item) => item && typeof item === 'object' && Number(item.clip_id) > 0)
    .map((item) => ({ ...item, clip_id: Number(item.clip_id) }))
}

// loadFeedProgress 读取场景进度；没有缓存或数据不可用时返回 null（并在可能时清理坏数据）。
export function loadFeedProgress(owner, scene) {
  const storage = getStorage()
  const key = storageKey(owner, scene)
  if (!storage || !key) return null
  let raw = null
  try {
    raw = storage.getItem(key)
  } catch {
    return null
  }
  if (!raw) return null

  let parsed = null
  try {
    parsed = JSON.parse(raw)
  } catch {
    parsed = null
  }
  // 版本不符或场景错位说明是旧结构/脏数据：清掉并按首屏处理。
  if (
    !parsed ||
    parsed.version !== FEED_PROGRESS_VERSION ||
    parsed.scene !== scene ||
    parsed.owner !== owner ||
    !Array.isArray(parsed.items) ||
    typeof parsed.cursor !== 'string' ||
    typeof parsed.hasMore !== 'boolean'
  ) {
    clearFeedProgress(owner, scene)
    return null
  }

  const items = sanitizeItems(parsed.items)
  if (items.length === 0) {
    clearFeedProgress(owner, scene)
    return null
  }
  return { items, cursor: parsed.cursor, hasMore: parsed.hasMore }
}

// saveFeedProgress 覆盖写入场景进度；写入失败（配额等）静默，只影响下次恢复。
export function saveFeedProgress(owner, scene, { items, cursor, hasMore }) {
  const storage = getStorage()
  const key = storageKey(owner, scene)
  if (!storage || !key || !Array.isArray(items) || items.length === 0) return
  try {
    storage.setItem(
      key,
      JSON.stringify({
        version: FEED_PROGRESS_VERSION,
        owner,
        scene,
        cursor: typeof cursor === 'string' ? cursor : '',
        hasMore: Boolean(hasMore),
        items,
      }),
    )
  } catch {
    // 存储不可写或超配额：保持内存态可用，下次进入按首屏处理。
  }
}
