// MessagesPage.jsx 渲染站内消息列表、未读提示与“全部已读”操作。
import React, { useEffect, useState } from 'react'
import Avatar from '../components/Avatar.jsx'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import Spinner from '../components/Spinner.jsx'
import Toast from '../components/Toast.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'
import { formatDateTime } from '../lib/format.js'

const KIND_LABELS = {
  followed: '关注了你',
}

export default function MessagesPage({ unread, onUnreadChange }) {
  const messages = useApi(() => api.messages({ limit: 20 }), [])
  const [items, setItems] = useState([])
  const [marking, setMarking] = useState(false)
  const [feedback, setFeedback] = useState(null)

  useEffect(() => {
    if (messages.data) setItems(messages.data.items || [])
  }, [messages.data])

  // 进入消息页时同步服务端未读数，避免顶栏角标与页面提示沿用登录时的旧值。
  useEffect(() => {
    let alive = true
    api
      .unread()
      .then((data) => {
        if (alive) onUnreadChange((data && data.unread) || 0)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [onUnreadChange])

  async function handleMarkAllRead() {
    if (marking) return
    setMarking(true)
    setFeedback(null)
    try {
      await api.markAllRead()
      setItems((prev) => prev.map((message) => ({ ...message, read: true })))
      onUnreadChange(0)
      setFeedback({ text: '已全部标记为已读', tone: 'success' })
    } catch (err) {
      setFeedback({ text: err.message, tone: 'error' })
    } finally {
      setMarking(false)
    }
  }

  const showSkeleton = messages.loading && items.length === 0
  const showEmpty = !messages.loading && !messages.error && items.length === 0

  return (
    <section className="pf-list-page" aria-labelledby="pf-messages-title">
      <div className="pf-page-head">
        <div>
          <h1 id="pf-messages-title" className="pf-page-title">
            消息
          </h1>
          <p className="pf-page-hint">
            未读 <span className="pf-num">{unread}</span> 条
          </p>
        </div>
        <button
          type="button"
          className="pf-ghost-btn"
          onClick={handleMarkAllRead}
          disabled={marking || unread === 0}
        >
          {marking ? <Spinner label="正在标记已读" /> : null}
          {marking ? '处理中…' : '全部标记已读'}
        </button>
      </div>

      <ErrorBanner message={messages.error} onRetry={messages.retry} />

      {feedback ? (
        <Toast message={feedback.text} tone={feedback.tone} onDismiss={() => setFeedback(null)} />
      ) : null}

      {showSkeleton ? (
        <div className="pf-message-list" aria-busy="true" aria-label="消息加载中">
          {[0, 1, 2].map((key) => (
            <div key={key} className="pf-message pf-message-skeleton">
              <span className="pf-sk-avatar" />
              <span className="pf-sk-line" />
            </div>
          ))}
        </div>
      ) : null}

      {showEmpty ? (
        <EmptyState
          icon="mail"
          title="还没有消息"
          description="当有人关注你时，消息会出现在这里。"
        />
      ) : null}

      {items.length > 0 ? (
        <ul className="pf-message-list" aria-label="消息列表">
          {items.map((message) => (
            <li key={message.id} className={message.read ? 'pf-message' : 'pf-message is-unread'}>
              <Avatar name={`用户 ${message.actor_id}`} />
              <div className="pf-message-main">
                <p className="pf-message-text">
                  <span className="pf-message-actor">用户 {message.actor_id}</span>
                  {KIND_LABELS[message.kind] || message.kind}
                </p>
                <span className="pf-message-time">{formatDateTime(message.created_at)}</span>
              </div>
              {message.read ? null : (
                <span className="pf-message-unread" aria-label="未读">
                  <span aria-hidden="true" />
                </span>
              )}
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  )
}
