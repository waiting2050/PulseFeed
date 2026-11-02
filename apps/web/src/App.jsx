// App.jsx 提供 S18 最小闭环：登录/注册、latest Feed 首屏与 next_cursor 加载更多（按 clip_id 去重）。
import React, { useCallback, useEffect, useState } from 'react'

const TOKEN_KEY = 'pulsefeed.token'

// 生成幂等键：优先使用浏览器 UUID，缺失时退化为时间戳 + 随机串。
function newIdempotencyKey() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return `pf-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// apiFetch 统一封装 API 访问：自动携带 Bearer，写请求自动带 Idempotency-Key，并解包 {code,message,data}。
async function apiFetch(path, { method = 'GET', body, token, idempotencyKey } = {}) {
  const headers = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (token) headers.Authorization = `Bearer ${token}`
  if (method !== 'GET' && method !== 'HEAD') {
    headers['Idempotency-Key'] = idempotencyKey || newIdempotencyKey()
  }

  const response = await fetch(`/api${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  let payload = null
  try {
    payload = await response.json()
  } catch {
    payload = null
  }
  if (!response.ok || (payload && payload.code !== 0)) {
    throw new Error((payload && payload.message) || `请求失败 (${response.status})`)
  }
  return payload ? payload.data : null
}

// FeedCard 渲染单条 clip 卡片，字段与 GET /api/feed 的 data.items 对齐。
function FeedCard({ item }) {
  const published = item.published_at ? new Date(item.published_at).toLocaleString() : ''
  return (
    <article className="card">
      <div className="card-cover">
        {item.cover_url ? (
          <img src={item.cover_url} alt={item.title || 'cover'} loading="lazy" />
        ) : (
          <span className="card-cover-fallback">#{item.clip_id}</span>
        )}
      </div>
      <div className="card-body">
        <h3 className="card-title">{item.title || '未命名 clip'}</h3>
        <p className="card-meta">
          <span>创作者 {item.creator_id}</span>
          <span>{published}</span>
        </p>
        <p className="card-stats">
          <span>播放 {item.play_count}</span>
          <span>点赞 {item.like_count}</span>
          <span>收藏 {item.save_count}</span>
          <span>评论 {item.comment_count}</span>
        </p>
      </div>
    </article>
  )
}

export default function App() {
  const [token, setToken] = useState(() => localStorage.getItem(TOKEN_KEY) || '')
  const [mode, setMode] = useState('login')
  const [account, setAccount] = useState('')
  const [password, setPassword] = useState('')
  const [nickname, setNickname] = useState('')
  const [authError, setAuthError] = useState('')
  const [busy, setBusy] = useState(false)

  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [feedError, setFeedError] = useState('')
  const [feedLoading, setFeedLoading] = useState(false)

  // 按 clip_id 追加去重，避免翻页期间重复渲染同一条。
  const appendItems = useCallback((incoming) => {
    setItems((prev) => {
      const seen = new Set(prev.map((item) => item.clip_id))
      const merged = [...prev]
      for (const item of incoming) {
        if (!seen.has(item.clip_id)) {
          seen.add(item.clip_id)
          merged.push(item)
        }
      }
      return merged
    })
  }, [])

  // loadFeed 拉取一页 latest Feed；reset=true 表示首屏或切换账号后重置。
  const loadFeed = useCallback(
    async (nextToken, nextCursor, reset) => {
      setFeedLoading(true)
      setFeedError('')
      try {
        const params = new URLSearchParams({ scene: 'latest', limit: '10' })
        if (nextCursor) params.set('cursor', nextCursor)
        const data = await apiFetch(`/feed?${params.toString()}`, { token: nextToken })
        const page = (data && data.items) || []
        if (reset) {
          setItems(page)
        } else {
          appendItems(page)
        }
        setCursor((data && data.next_cursor) || '')
        setHasMore(Boolean(data && data.has_more))
      } catch (err) {
        setFeedError(err.message)
      } finally {
        setFeedLoading(false)
      }
    },
    [appendItems],
  )

  useEffect(() => {
    if (token) {
      loadFeed(token, '', true)
    }
  }, [token, loadFeed])

  async function handleAuth(event) {
    event.preventDefault()
    setAuthError('')
    setBusy(true)
    try {
      if (mode === 'register') {
        await apiFetch('/users', {
          method: 'POST',
          body: { account, password, nickname: nickname || undefined },
        })
      }
      const data = await apiFetch('/sessions', {
        method: 'POST',
        body: { account, password },
      })
      const nextToken = (data && data.access_token) || ''
      if (!nextToken) throw new Error('登录响应缺少 access_token')
      localStorage.setItem(TOKEN_KEY, nextToken)
      setToken(nextToken)
    } catch (err) {
      setAuthError(err.message)
    } finally {
      setBusy(false)
    }
  }

  function handleLogout() {
    localStorage.removeItem(TOKEN_KEY)
    setToken('')
    setItems([])
    setCursor('')
    setHasMore(false)
    setFeedError('')
  }

  async function handleLoadMore() {
    if (!token || !hasMore || feedLoading) return
    await loadFeed(token, cursor, false)
  }

  if (!token) {
    return (
      <main className="page">
        <header className="hero">
          <h1>PulseFeed</h1>
          <p>轻量 Feed 系统 · 登录后查看最新发布</p>
        </header>
        <section className="panel">
          <div className="tabs">
            <button
              type="button"
              className={mode === 'login' ? 'tab active' : 'tab'}
              onClick={() => setMode('login')}
            >
              登录
            </button>
            <button
              type="button"
              className={mode === 'register' ? 'tab active' : 'tab'}
              onClick={() => setMode('register')}
            >
              注册
            </button>
          </div>
          <form className="auth-form" onSubmit={handleAuth}>
            <label>
              账号
              <input
                value={account}
                onChange={(event) => setAccount(event.target.value)}
                autoComplete="username"
                required
              />
            </label>
            <label>
              密码
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="current-password"
                required
              />
            </label>
            {mode === 'register' && (
              <label>
                昵称（可选）
                <input
                  value={nickname}
                  onChange={(event) => setNickname(event.target.value)}
                />
              </label>
            )}
            {authError && <p className="error">{authError}</p>}
            <button type="submit" className="primary" disabled={busy}>
              {busy ? '处理中…' : mode === 'login' ? '登录' : '注册并登录'}
            </button>
          </form>
        </section>
      </main>
    )
  }

  return (
    <main className="page">
      <header className="topbar">
        <div>
          <h1>PulseFeed</h1>
          <p className="subtle">latest Feed</p>
        </div>
        <button type="button" className="ghost" onClick={handleLogout}>
          退出登录
        </button>
      </header>

      {feedError && <p className="error">{feedError}</p>}
      {items.length === 0 && !feedLoading && !feedError && (
        <p className="empty">暂无可见 clip，先发布一条再刷新。</p>
      )}

      <section className="feed">
        {items.map((item) => (
          <FeedCard key={item.clip_id} item={item} />
        ))}
      </section>

      <footer className="load-more">
        {hasMore ? (
          <button type="button" className="primary" onClick={handleLoadMore} disabled={feedLoading}>
            {feedLoading ? '加载中…' : '加载更多'}
          </button>
        ) : (
          items.length > 0 && <p className="subtle">已经到底了</p>
        )}
      </footer>
    </main>
  )
}
