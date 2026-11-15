// feedProgress.js 把 Feed 各场景的时间线进度（已加载列表、游标、是否还有下一页）持久化到
// sessionStorage，让关页、刷新、从详情返回时接着上次的位置继续，而不是重新拉第一页。
// 每个场景独立分键；结构版本写在数据内，版本不符、解析失败或数据形状不对时直接丢弃并清理，
// 调用方据此回落到第一页，避免把用户卡在坏数据上。

// FEED_PROGRESS_VERSION 是持久化结构版本：字段或语义变化时递增，旧数据自动失效。
const FEED_PROGRESS_VERSION = 1
const KEY_PREFIX = 'pulsefeed.feed.v1.'

// storageKey 按场景分键，场景之间互不串列表。
function storageKey(scene) {
  return `${KEY_PREFIX}${scene}`
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
export function clearFeedProgress(scene) {
  const storage = getStorage()
  if (!storage || !scene) return
  try {
    storage.removeItem(storageKey(scene))
  } catch {
    // 存储不可写时无需处理：下次恢复读不到数据会自然回到第一页。
  }
}

// sanitizeItems 过滤掉缺 clip_id 的脏条目并规范化 ID 类型。
function sanitizeItems(items) {
  return items
    .filter((item) => item && typeof item === 'object' && Number(item.clip_id) > 0)
    .map((item) => ({ ...item, clip_id: Number(item.clip_id) }))
}

// loadFeedProgress 读取场景进度；没有缓存或数据不可用时返回 null（并在可能时清理坏数据）。
export function loadFeedProgress(scene) {
  const storage = getStorage()
  if (!storage || !scene) return null
  let raw = null
  try {
    raw = storage.getItem(storageKey(scene))
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
    !Array.isArray(parsed.items) ||
    typeof parsed.cursor !== 'string' ||
    typeof parsed.hasMore !== 'boolean'
  ) {
    clearFeedProgress(scene)
    return null
  }

  const items = sanitizeItems(parsed.items)
  if (items.length === 0) {
    clearFeedProgress(scene)
    return null
  }
  return { items, cursor: parsed.cursor, hasMore: parsed.hasMore }
}

// saveFeedProgress 覆盖写入场景进度；写入失败（配额等）静默，只影响下次恢复。
export function saveFeedProgress(scene, { items, cursor, hasMore }) {
  const storage = getStorage()
  if (!storage || !scene || !Array.isArray(items) || items.length === 0) return
  try {
    storage.setItem(
      storageKey(scene),
      JSON.stringify({
        version: FEED_PROGRESS_VERSION,
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
