// App.jsx 只保留应用外壳与登录态：顶栏、页面切换、用户资料与未读数；各页自行加载数据。
import React, { useCallback, useEffect, useState } from 'react'
import TopBar from './components/TopBar.jsx'
import LoginPage from './pages/LoginPage.jsx'
import FeedPage from './pages/FeedPage.jsx'
import PublishPage from './pages/PublishPage.jsx'
import MessagesPage from './pages/MessagesPage.jsx'
import PlaybackPage from './pages/PlaybackPage.jsx'
import { api, getToken, hydrateToken, setToken } from './lib/api.js'

export default function App() {
  const [token, setTokenState] = useState(() => hydrateToken())
  const [me, setMe] = useState(null)
  const [tab, setTab] = useState('feed')
  const [scene, setScene] = useState('latest')
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
      setTab('feed')
      setScene('latest')
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
    setToken('')
    setTokenState('')
    setMe(null)
    setUnread(0)
    setTab('feed')
  }

  const handleUnreadChange = useCallback((value) => setUnread(value), [])

  if (!token || !getToken()) {
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
        {tab === 'feed' ? (
          <FeedPage scene={scene} onScene={setScene} onGoPublish={() => setTab('publish')} />
        ) : null}
        {tab === 'publish' ? <PublishPage onPublished={() => setTab('feed')} /> : null}
        {tab === 'messages' ? <MessagesPage unread={unread} onUnreadChange={handleUnreadChange} /> : null}
        {tab === 'playback' ? <PlaybackPage /> : null}
      </main>
    </div>
  )
}
