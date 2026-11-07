// App.jsx 只保留应用外壳与登录态：顶栏、路由页面、用户资料与未读数；各页自行加载数据。
// 路由把原先的 tab/scene 状态搬进 URL，刷新与前进后退都能还原当前页面。
import React, { useCallback, useEffect, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import TopBar from './components/TopBar.jsx'
import EmptyState from './components/EmptyState.jsx'
import LoginPage from './pages/LoginPage.jsx'
import FeedPage from './pages/FeedPage.jsx'
import PublishPage from './pages/PublishPage.jsx'
import MessagesPage from './pages/MessagesPage.jsx'
import PlaybackPage from './pages/PlaybackPage.jsx'
import ProfilePage from './pages/ProfilePage.jsx'
import { api, getToken, hydrateToken, invalidatePlaybackConfig, loadPlaybackConfig, setToken } from './lib/api.js'

// readNext 只接受站内绝对路径，避免登录回跳被外部地址利用。
function readNext(search) {
  const value = new URLSearchParams(search).get('next')
  if (!value || !value.startsWith('/') || value.startsWith('//')) return ''
  return value
}

function tabForPath(pathname) {
  if (pathname.startsWith('/publish')) return 'publish'
  if (pathname.startsWith('/messages')) return 'messages'
  if (pathname.startsWith('/playback')) return 'playback'
  return 'feed'
}

// NotFound 兜住未匹配路径，给出返回内容流的入口而不是白屏。
function NotFound({ onBack }) {
  return (
    <div className="pf-feed-page">
      <EmptyState
        title="页面不存在"
        description="这个地址没有对应的页面，返回内容流继续浏览。"
        actionLabel="回到内容流"
        onAction={onBack}
      />
    </div>
  )
}

export default function App() {
  const location = useLocation()
  const navigate = useNavigate()
  const [token, setTokenState] = useState(() => hydrateToken())
  const [me, setMe] = useState(null)
  const [unread, setUnread] = useState(0)

  const [mode, setMode] = useState('login')
  const [account, setAccount] = useState('')
  const [password, setPassword] = useState('')
  const [nickname, setNickname] = useState('')
  const [authError, setAuthError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!token) {
      setMe(null)
      setUnread(0)
      return undefined
    }
    let alive = true
    api
      .me()
      .then((data) => {
        if (alive) setMe(data)
      })
      .catch(() => {})
    api
      .unread()
      .then((data) => {
        if (alive) setUnread((data && data.unread) || 0)
      })
      .catch(() => {})
    // 登录后读取一次播放配置并缓存，播放结算直接复用，不在每次播放时请求。
    loadPlaybackConfig().catch(() => {})
    return () => {
      alive = false
    }
  }, [token])

  async function handleAuth(event) {
    event.preventDefault()
    setAuthError('')
    setBusy(true)
    try {
      if (mode === 'register') {
        await api.register({ account, password, nickname })
      }
      const data = await api.login({ account, password })
      const nextToken = (data && data.access_token) || ''
      if (!nextToken) throw new Error('登录响应缺少 access_token')
      setToken(nextToken)
      setTokenState(nextToken)
      navigate(readNext(location.search) || '/feed', { replace: true })
    } catch (err) {
      setAuthError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function handleLogout() {
    try {
      await api.logout()
    } catch {
      // 登出失败不阻塞本地状态清理。
    }
    invalidatePlaybackConfig()
    setToken('')
    setTokenState('')
    setMe(null)
    setUnread(0)
    navigate('/login', { replace: true })
  }

  const handleUnreadChange = useCallback((value) => setUnread(value), [])

  const handleTab = useCallback(
    (key) => {
      navigate(key === 'feed' ? '/feed' : `/${key}`)
    },
    [navigate],
  )

  if (!token || !getToken()) {
    return (
      <Routes>
        <Route
          path="/login"
          element={
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
          }
        />
        {/* 未登录访问任意深链都先去登录，登录成功后回到原地址。 */}
        <Route
          path="*"
          element={
            <Navigate
              to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`}
              replace
            />
          }
        />
      </Routes>
    )
  }

  return (
    <div className="pf-app">
      <TopBar
        tab={tabForPath(location.pathname)}
        onTab={handleTab}
        unread={unread}
        me={me}
        onLogout={handleLogout}
        onProfile={() => navigate('/me')}
      />

      <main className="pf-main">
        <Routes>
          <Route index element={<Navigate to="/feed" replace />} />
          <Route path="/login" element={<Navigate to="/feed" replace />} />
          <Route
            path="/feed"
            element={
              <FeedPage
                me={me}
                onGoPublish={() => navigate('/publish')}
                onOpenUser={(userId) => navigate(`/users/${userId}`)}
              />
            }
          />
          <Route path="/publish" element={<PublishPage onPublished={() => navigate('/feed')} />} />
          <Route path="/messages" element={<MessagesPage unread={unread} onUnreadChange={handleUnreadChange} />} />
          <Route path="/playback" element={<PlaybackPage />} />
          <Route path="/me" element={<ProfilePage me={me} onMeChange={setMe} />} />
          <Route path="/users/:userId" element={<ProfilePage me={me} onMeChange={setMe} />} />
          {/* 单条内容深链：详情弹层由 FeedPage 按 URL 渲染，列表仍留在背后作为关闭后的落点。 */}
          <Route
            path="/clip/:clipId"
            element={
              <FeedPage
                me={me}
                onGoPublish={() => navigate('/publish')}
                onOpenUser={(userId) => navigate(`/users/${userId}`)}
              />
            }
          />
          <Route path="*" element={<NotFound onBack={() => navigate('/feed')} />} />
        </Routes>
      </main>
    </div>
  )
}
