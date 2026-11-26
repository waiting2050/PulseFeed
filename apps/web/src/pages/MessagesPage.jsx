// MessagesPage.jsx 渲染站内消息列表：按类型展示图标与标签、标题正文、未读态，
// 支持单条点击标记已读与“全部已读”，并同步顶栏未读徽章。
import React, { useEffect, useState } from 'react'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import Icon from '../components/Icon.jsx'
import Spinner from '../components/Spinner.jsx'
import Toast from '../components/Toast.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'
import { formatDateTime } from '../lib/format.js'

// KIND_META 覆盖后端四类通知：LIKE / COMMENT / FOLLOW / SYSTEM。
// followed 是旧数据兼容标签，后端读取时已归一化为 FOLLOW，这里保留兜底不影响展示。
const KIND_META = {
  LIKE: { label: '点赞', icon: 'heart' },
  COMMENT: { label: '评论', icon: 'comment' },
  FOLLOW: { label: '关注', icon: 'user' },
  SYSTEM: { label: '系统', icon: 'bell' },
  followed: { label: '关注', icon: 'user' },
}

const FALLBACK_META = { label: '消息', icon: 'mail' }

function messageMeta(kind) {
  return KIND_META[kind] || FALLBACK_META
}

export default function MessagesPage({ unread, onUnreadChange }) {
  const messages = useApi(() => api.messages({ limit: 20 }), [])
  const [items, setItems] = useState([])
  const [marking, setMarking] = useState(false)
  const [markingId, setMarkingId] = useState(0)
  const [feedback, setFeedback] = useState(null)

  useEffect(() => {
    if (messages.data) setItems(messages.data.items || [])
  }, [messages.data])

  // 以服务端未读数为准刷新顶栏角标，避免本地累加与服务端口径漂移。
  async function refreshUnread() {
    try {
      const data = await api.unread()
      onUnreadChange((data && data.unread) || 0)
    } catch {
      // 未读数刷新失败不阻塞已读操作，下次进入页面会再次同步。
    }
  }

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

  async function handleMarkRead(message) {
    if (message.read || marking || markingId) return
    setMarkingId(message.id)
    setFeedback(null)
    try {
      await api.markRead([message.id])
      setItems((prev) =>
        prev.map((item) =>
          item.id === message.id
            ? { ...item, read: true, read_at: item.read_at || new Date().toISOString() }
            : item,
        ),
      )
      await refreshUnread()
    } catch (err) {
      setFeedback({ text: err.message, tone: 'error' })
    } finally {
      setMarkingId(0)
    }
  }

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
          description="当有人关注、点赞或评论你的内容时，消息会出现在这里。"
        />
      ) : null}

      {items.length > 0 ? (
        <ul className="pf-message-list" aria-label="消息列表">
          {items.map((message) => {
            const meta = messageMeta(message.kind)
            const busy = markingId === message.id
            return (
              <li key={message.id}>
                <button
                  type="button"
                  className={message.read ? 'pf-message' : 'pf-message is-unread'}
                  onClick={() => handleMarkRead(message)}
                  disabled={message.read || marking || busy}
                >
                  <span
                    className={`pf-message-icon is-${String(message.kind || 'system').toLowerCase()}`}
                    aria-hidden="true"
                  >
                    {busy ? <Spinner label="正在标记已读" /> : <Icon name={meta.icon} size={18} />}
                  </span>
                  <span className="pf-message-main">
                    <span className="pf-message-head">
                      <span className="pf-message-type">{meta.label}</span>
                      <span className="pf-message-title">{message.title || meta.label}</span>
                    </span>
                    {message.content ? <span className="pf-message-text">{message.content}</span> : null}
                    <span className="pf-message-time">
                      {formatDateTime(message.created_at)}
                      {message.actor_id > 0 ? ` · 用户 ${message.actor_id}` : ''}
                    </span>
                  </span>
                  {message.read ? null : (
                    <span className="pf-message-unread" aria-label="未读">
                      <span aria-hidden="true" />
                    </span>
                  )}
                </button>
              </li>
            )
          })}
        </ul>
      ) : null}
    </section>
  )
}
