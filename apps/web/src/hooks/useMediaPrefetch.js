// useMediaPrefetch.js 在 Feed 首屏数据就绪后，按服务端预加载配置预热后续内容的封面与视频元信息。
// 预取只发请求不渲染、失败静默；并发上限 3，场景切换或离开页面时通过 AbortController 统一取消。
import { useEffect } from 'react'
import { api } from '../lib/api.js'

const CONCURRENCY = 3

// prefetchCover 预取封面并读完响应体，使浏览器 HTTP 缓存命中后续 <img>。
async function prefetchCover(url, signal) {
  if (!url || signal.aborted) return
  try {
    const response = await fetch(url, { signal })
    if (response.ok) await response.blob()
  } catch {
    // 预取失败静默，不打扰用户。
  }
}

// prefetchVideo 用游离的 <video preload="metadata"> 只拉取视频头部元信息，完成后清空 src 释放连接。
// 5 秒超时兜底：浏览器对并发视频元素的 metadata 事件可能不回调，超时即释放，避免阻塞后续队列。
function prefetchVideo(url, signal) {
  if (!url || signal.aborted) return Promise.resolve()
  return new Promise((resolve) => {
    const video = document.createElement('video')
    let settled = false
    let timer = 0
    const finish = () => {
      if (settled) return
      settled = true
      window.clearTimeout(timer)
      video.removeEventListener('loadedmetadata', finish)
      video.removeEventListener('error', finish)
      signal.removeEventListener('abort', finish)
      video.src = ''
      video.load()
      resolve()
    }
    signal.addEventListener('abort', finish, { once: true })
    video.addEventListener('loadedmetadata', finish, { once: true })
    video.addEventListener('error', finish, { once: true })
    video.preload = 'metadata'
    video.muted = true
    video.playsInline = true
    video.src = url
    video.load()
    timer = window.setTimeout(finish, 5000)
  })
}

// useMediaPrefetch 拉取 /api/preload-clips 并按固定并发预热；enabled 为 false 时不发起请求。
// deps 用于控制取消时机（例如场景或首屏数据变化），hook 内部不读取 deps。
export default function useMediaPrefetch(enabled, deps = []) {
  useEffect(() => {
    if (!enabled) return undefined

    const controller = new AbortController()
    const { signal } = controller
    let cursor = 0

    async function run() {
      let items = []
      try {
        const data = await api.preloadClips({ signal })
        items = (data && data.items) || []
      } catch {
        return
      }
      if (signal.aborted) return

      const workerCount = Math.min(CONCURRENCY, items.length)
      const workers = Array.from({ length: workerCount }, async () => {
        while (!signal.aborted) {
          const index = cursor
          cursor += 1
          if (index >= items.length) return
          const item = items[index]
          await Promise.all([
            prefetchCover(item.cover_url, signal),
            prefetchVideo(item.media_url, signal),
          ])
        }
      })
      await Promise.all(workers)
    }

    run()
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, enabled])
}
