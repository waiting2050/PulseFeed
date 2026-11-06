// App.jsx 负责数据与状态：登录、四场景 Feed、发布、互动、消息与播放配置；视图交由 P0 组件渲染。
import React, { useCallback, useEffect, useRef, useState } from 'react'
import TopBar from './components/TopBar.jsx'
import LoginPage from './pages/LoginPage.jsx'
import FeedPage from './pages/FeedPage.jsx'

const TOKEN_KEY = 'pulsefeed.token'

// 生成幂等键：优先使用浏览器 UUID，缺失时退化为时间戳 + 随机串。
function newIdempotencyKey() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return `pf-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// readEnvelope 解析统一信封 {code,message,data}，失败时抛出后端 message。
async function readEnvelope(response) {
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

// apiFetch 统一封装 API 访问：自动携带 Bearer，写请求自动带 Idempotency-Key。
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
  return readEnvelope(response)
}

// apiUpload 以 multipart 上传文件（视频/封面），返回 {url, kind}。
async function apiUpload(path, formData, token) {
  const response = await fetch(`/api${path}`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Idempotency-Key': newIdempotencyKey(),
    },
    body: formData,
  })
  return readEnvelope(response)
}

export default function App() {
  const [token, setToken] = useState(() => localStorage.getItem(TOKEN_KEY) || '')
  const [me, setMe] = useState(null)
  const [mode, setMode] = useState('login')
  const [account, setAccount] = useState('')
  const [password, setPassword] = useState('')
  const [nickname, setNickname] = useState('')
  const [authError, setAuthError] = useState('')
  const [busy, setBusy] = useState(false)
  const [tab, setTab] = useState('feed')

  const [scene, setScene] = useState('latest')
  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [feedError, setFeedError] = useState('')
  const [feedLoading, setFeedLoading] = useState(false)
  const [comments, setComments] = useState({})
  const feedSeq = useRef(0)

  const [title, setTitle] = useState('')
  const [videoFile, setVideoFile] = useState(null)
  const [coverFile, setCoverFile] = useState(null)
  const [publishNotice, setPublishNotice] = useState('')
  const [publishError, setPublishError] = useState('')
  const [publishing, setPublishing] = useState(false)

  const [messages, setMessages] = useState([])
  const [unread, setUnread] = useState(0)
  const [messageError, setMessageError] = useState('')
  const [messagesLoading, setMessagesLoading] = useState(false)

  const [playback, setPlayback] = useState({ preload_count: 0, qos_sample_rate: 0 })
  const [preloadIds, setPreloadIds] = useState([])
  const [playbackError, setPlaybackError] = useState('')
  const [playbackNotice, setPlaybackNotice] = useState('')
  const [playbackLoading, setPlaybackLoading] = useState(false)

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

  // loadFeed 拉取指定场景的一页；reset=true 时用请求序号丢弃过期响应。
  const loadFeed = useCallback(
    async (nextToken, nextScene, nextCursor, reset) => {
      const seq = reset ? ++feedSeq.current : feedSeq.current
      setFeedLoading(true)
      setFeedError('')
      try {
        const params = new URLSearchParams({ scene: nextScene, limit: '10' })
        if (nextCursor) params.set('cursor', nextCursor)
        const data = await apiFetch(`/feed?${params.toString()}`, { token: nextToken })
        if (seq !== feedSeq.current) return
        const page = (data && data.items) || []
        if (reset) {
          setItems(page)
        } else {
          appendItems(page)
        }
        setCursor((data && data.next_cursor) || '')
        setHasMore(Boolean(data && data.has_more))
      } catch (err) {
        if (seq === feedSeq.current) setFeedError(err.message)
      } finally {
        if (seq === feedSeq.current) setFeedLoading(false)
      }
    },
    [appendItems],
  )

  // 登录后加载用户资料、Feed 与未读数。
  useEffect(() => {
    if (!token) {
      setMe(null)
      return undefined
    }
    let alive = true
    apiFetch('/users/me', { token })
      .then((data) => {
        if (alive) setMe(data)
      })
      .catch(() => {})
    apiFetch('/message-stats/unread', { token })
      .then((data) => {
        if (alive) setUnread((data && data.unread) || 0)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [token])

  useEffect(() => {
    if (token) loadFeed(token, scene, '', true)
  }, [token, scene, loadFeed])

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
      setTab('feed')
    } catch (err) {
      setAuthError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function handleLogout() {
    try {
      await apiFetch('/sessions/current', { method: 'DELETE', token })
    } catch {
      // 登出失败不阻塞本地状态清理。
    }
    localStorage.removeItem(TOKEN_KEY)
    setToken('')
    setMe(null)
    setItems([])
    setCursor('')
    setHasMore(false)
    setComments({})
    setMessages([])
    setUnread(0)
  }

  async function handleLoadMore() {
    if (!token || !hasMore || feedLoading) return
    await loadFeed(token, scene, cursor, false)
  }

  function patchItem(clipId, patch) {
    setItems((prev) => prev.map((item) => (item.clip_id === clipId ? { ...item, ...patch } : item)))
  }

  // 点赞采用乐观更新：先改本地计数与状态，失败后回滚。
  async function handleLike(item) {
    const liked = !item.liked
    const likeCount = Math.max(0, item.like_count + (liked ? 1 : -1))
    patchItem(item.clip_id, { liked, like_count: likeCount })
    try {
      const data = await apiFetch(`/clips/${item.clip_id}/like`, {
        method: liked ? 'PUT' : 'DELETE',
        token,
      })
      patchItem(item.clip_id, { like_count: data.like_count, liked: data.active })
    } catch (err) {
      patchItem(item.clip_id, { liked: item.liked, like_count: item.like_count })
      setFeedError(err.message)
    }
  }

  // 收藏同样乐观更新并按服务端计数校正。
  async function handleSave(item) {
    const saved = !item.saved
    const saveCount = Math.max(0, item.save_count + (saved ? 1 : -1))
    patchItem(item.clip_id, { saved, save_count: saveCount })
    try {
      const data = await apiFetch(`/clips/${item.clip_id}/save`, {
        method: saved ? 'PUT' : 'DELETE',
        token,
      })
      patchItem(item.clip_id, { save_count: data.save_count, saved: data.active })
    } catch (err) {
      patchItem(item.clip_id, { saved: item.saved, save_count: item.save_count })
      setFeedError(err.message)
    }
  }

  async function handleToggleComments(clipId) {
    const current = comments[clipId]
    if (current && current.open) {
      setComments((prev) => ({ ...prev, [clipId]: { ...current, open: false } }))
      return
    }
    setComments((prev) => ({
      ...prev,
      [clipId]: { open: true, items: (current && current.items) || [], loading: true, error: '' },
    }))
    try {
      const data = await apiFetch(`/clips/${clipId}/comments?limit=20`, { token })
      const list = (data && data.items) || []
      setComments((prev) => ({ ...prev, [clipId]: { open: true, items: list, loading: false, error: '' } }))
    } catch (err) {
      setComments((prev) => ({
        ...prev,
        [clipId]: { open: true, items: [], loading: false, error: err.message },
      }))
    }
  }

  // 评论乐观追加临时项，成功后替换为服务端评论，失败则移除并回滚计数。
  async function handleSubmitComment(clipId, content) {
    const tempId = `tmp-${Date.now()}`
    const temp = {
      id: tempId,
      clip_id: clipId,
      user_id: (me && me.user_id) || 0,
      user_nickname: (me && me.nickname) || '我',
      content,
      created_at: new Date().toISOString(),
    }
    setComments((prev) => {
      const current = prev[clipId] || { open: true, items: [], loading: false, error: '' }
      return {
        ...prev,
        [clipId]: { ...current, open: true, items: [...current.items, temp], error: '' },
      }
    })
    setItems((prev) =>
      prev.map((item) =>
        item.clip_id === clipId ? { ...item, comment_count: item.comment_count + 1 } : item,
      ),
    )
    try {
      const data = await apiFetch(`/clips/${clipId}/comments`, {
        method: 'POST',
        body: { content },
        token,
      })
      setComments((prev) => {
        const current = prev[clipId]
        return {
          ...prev,
          [clipId]: {
            ...current,
            items: current.items.map((comment) => (comment.id === tempId ? data : comment)),
          },
        }
      })
      patchItem(clipId, { comment_count: data.comment_count })
    } catch (err) {
      setComments((prev) => {
        const current = prev[clipId]
        return {
          ...prev,
          [clipId]: {
            ...current,
            items: current.items.filter((comment) => comment.id !== tempId),
            error: err.message,
          },
        }
      })
      setItems((prev) =>
        prev.map((item) =>
          item.clip_id === clipId
            ? { ...item, comment_count: Math.max(0, item.comment_count - 1) }
            : item,
        ),
      )
    }
  }

  // 发布：先上传 clip 文件（可选封面），再创建 clip；成功后回到最新 Feed 刷新。
  async function handlePublish(event) {
    event.preventDefault()
    setPublishError('')
    setPublishNotice('')
    if (!title.trim()) {
      setPublishError('请填写标题')
      return
    }
    if (!videoFile) {
      setPublishError('请选择要上传的 clip 文件')
      return
    }
    setPublishing(true)
    try {
      const videoForm = new FormData()
      videoForm.append('file', videoFile)
      videoForm.append('kind', 'video')
      const video = await apiUpload('/uploads', videoForm, token)

      let coverURL = ''
      if (coverFile) {
        const coverForm = new FormData()
        coverForm.append('file', coverFile)
        coverForm.append('kind', 'cover')
        const cover = await apiUpload('/uploads', coverForm, token)
        coverURL = cover.url
      }

      await apiFetch('/clips', {
        method: 'POST',
        token,
        body: { title: title.trim(), video_url: video.url, cover_url: coverURL },
      })
      setPublishNotice('发布成功，已返回最新 Feed。')
      setTitle('')
      setVideoFile(null)
      setCoverFile(null)
      setScene('latest')
      setTab('feed')
      await loadFeed(token, 'latest', '', true)
    } catch (err) {
      setPublishError(err.message)
    } finally {
      setPublishing(false)
    }
  }

  async function loadMessages() {
    if (!token) return
    setMessagesLoading(true)
    setMessageError('')
    try {
      const data = await apiFetch('/messages?limit=20', { token })
      setMessages((data && data.items) || [])
    } catch (err) {
      setMessageError(err.message)
    } finally {
      setMessagesLoading(false)
    }
  }

  async function loadUnread() {
    if (!token) return
    try {
      const data = await apiFetch('/message-stats/unread', { token })
      setUnread((data && data.unread) || 0)
    } catch {
      // 未读数失败不阻塞页面。
    }
  }

  async function handleMarkAllRead() {
    if (!token) return
    try {
      await apiFetch('/messages', { method: 'PATCH', token })
      setUnread(0)
      setMessages((prev) => prev.map((message) => ({ ...message, read: true })))
    } catch (err) {
      setMessageError(err.message)
    }
  }

  async function loadPlayback() {
    if (!token) return
    setPlaybackLoading(true)
    setPlaybackError('')
    try {
      const data = await apiFetch('/playback-config', { token })
      setPlayback({
        preload_count: (data && data.preload_count) || 0,
        qos_sample_rate: (data && data.qos_sample_rate) || 0,
      })
    } catch (err) {
      setPlaybackError(err.message)
    } finally {
      setPlaybackLoading(false)
    }
  }

  async function handleSavePlayback(event) {
    event.preventDefault()
    setPlaybackNotice('')
    setPlaybackError('')
    try {
      const data = await apiFetch('/playback-config', {
        method: 'PUT',
        token,
        body: {
          preload_count: Number(playback.preload_count),
          qos_sample_rate: Number(playback.qos_sample_rate),
        },
      })
      setPlayback({
        preload_count: data.preload_count,
        qos_sample_rate: data.qos_sample_rate,
      })
      setPlaybackNotice('配置已保存')
    } catch (err) {
      setPlaybackError(err.message)
    }
  }

  async function handleLoadPreload() {
    setPlaybackError('')
    try {
      const data = await apiFetch('/preload-clips', { token })
      setPreloadIds((data && data.clip_ids) || [])
    } catch (err) {
      setPlaybackError(err.message)
    }
  }

  // 切换到消息/播放页时按需拉取数据。
  useEffect(() => {
    if (tab === 'messages' && token) {
      loadMessages()
      loadUnread()
    }
    if (tab === 'playback' && token) {
      loadPlayback()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, token])

  if (!token) {
    return (
      <LoginPage
        mode={mode}
        onMode={setMode}
        account={account}
        onAccount={setAccount}
        password={password}
        onPassword={setPassword}
        nickname={nickname}
        onNickname={setNickname}
        authError={authError}
        busy={busy}
        onSubmit={handleAuth}
      />
    )
  }

  return (
    <div className="pf-app">
      <TopBar tab={tab} onTab={setTab} unread={unread} me={me} onLogout={handleLogout} />

      <main className="pf-main">
        {tab === 'feed' && (
          <FeedPage
            scene={scene}
            onScene={setScene}
            items={items}
            loading={feedLoading}
            error={feedError}
            hasMore={hasMore}
            onLoadMore={handleLoadMore}
            onRetry={() => loadFeed(token, scene, '', true)}
            comments={comments}
            busy={busy}
            onLike={handleLike}
            onSave={handleSave}
            onToggleComments={handleToggleComments}
            onSubmitComment={handleSubmitComment}
            onGoPublish={() => setTab('publish')}
          />
        )}

      {tab === 'publish' && (
        <section className="panel">
          <h2 className="section-title">上传并发布</h2>
          <form className="publish-form" onSubmit={handlePublish}>
            <label>
              标题
              <input value={title} onChange={(event) => setTitle(event.target.value)} required />
            </label>
            <label>
              clip 文件（mp4 / mov / webm）
              <input
                type="file"
                accept=".mp4,.mov,.webm,video/*"
                onChange={(event) => setVideoFile(event.target.files?.[0] || null)}
              />
            </label>
            <label>
              封面（可选，jpg / png / webp）
              <input
                type="file"
                accept=".jpg,.jpeg,.png,.webp,image/*"
                onChange={(event) => setCoverFile(event.target.files?.[0] || null)}
              />
            </label>
            {publishError && <p className="error">{publishError}</p>}
            {publishNotice && <p className="notice">{publishNotice}</p>}
            <button type="submit" className="primary" disabled={publishing}>
              {publishing ? '上传中…' : '上传并发布'}
            </button>
          </form>
        </section>
      )}

      {tab === 'messages' && (
        <section className="panel">
          <div className="panel-head">
            <h2 className="section-title">站内消息</h2>
            <button type="button" className="ghost" onClick={handleMarkAllRead} disabled={unread === 0}>
              全部标记已读
            </button>
          </div>
          {messageError && <p className="error">{messageError}</p>}
          {messagesLoading && <p className="subtle">加载中…</p>}
          {!messagesLoading && messages.length === 0 && <p className="subtle">暂无消息。</p>}
          <ul className="message-list">
            {messages.map((message) => (
              <li key={message.id} className={message.read ? 'message' : 'message unread'}>
                <span className="message-kind">{message.kind}</span>
                <span className="message-text">
                  来自用户 {message.actor_id} · 目标 {message.target_id}
                </span>
                <span className="subtle">{new Date(message.created_at).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'playback' && (
        <section className="panel">
          <h2 className="section-title">播放配置</h2>
          {playbackError && <p className="error">{playbackError}</p>}
          {playbackLoading && <p className="subtle">加载中…</p>}
          <form className="publish-form" onSubmit={handleSavePlayback}>
            <label>
              预加载条数
              <input
                type="number"
                min="0"
                value={playback.preload_count}
                onChange={(event) =>
                  setPlayback((prev) => ({ ...prev, preload_count: Number(event.target.value) }))
                }
              />
            </label>
            <label>
              QoS 采样率
              <input
                type="number"
                min="0"
                max="1"
                step="0.01"
                value={playback.qos_sample_rate}
                onChange={(event) =>
                  setPlayback((prev) => ({ ...prev, qos_sample_rate: Number(event.target.value) }))
                }
              />
            </label>
            <div className="inline-actions">
              <button type="submit" className="primary">
                保存配置
              </button>
              <button type="button" className="ghost" onClick={handleLoadPreload}>
                查看预加载列表
              </button>
            </div>
            {playbackNotice && <p className="notice">{playbackNotice}</p>}
            {preloadIds.length > 0 && <p className="subtle">预加载 clip_id：{preloadIds.join(', ')}</p>}
          </form>
        </section>
      )}
      </main>
    </div>
  )
}
