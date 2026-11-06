// ClipCard.jsx 渲染 Feed 卡片：16:9 封面、无图渐变首字占位、作者行、操作栏与评论区。
import React, { useState } from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'

// 无封面时按 clip_id 稳定取一组低饱和暗色块，保证同一内容每次颜色一致且不抢标题。
const GRADIENTS = [
  'linear-gradient(140deg, #1c1d22 0%, #2e2a2d 100%)',
  'linear-gradient(140deg, #171f22 0%, #1f3034 100%)',
  'linear-gradient(140deg, #211c16 0%, #33281c 100%)',
  'linear-gradient(140deg, #161f19 0%, #233028 100%)',
  'linear-gradient(140deg, #1c1822 0%, #2a2336 100%)',
  'linear-gradient(140deg, #221a1c 0%, #33272a 100%)',
]

function hashOf(value) {
  const text = String(value == null ? '' : value)
  let hash = 0
  for (let i = 0; i < text.length; i += 1) {
    hash = (hash * 31 + text.charCodeAt(i)) % 997
  }
  return hash
}

// formatCount 将计数压成 1.2w / 1.1k，保持四位以内并保留两位小数以内的观感。
export function formatCount(value) {
  const count = Number(value) || 0
  if (count >= 10000) {
    const wan = count / 10000
    return `${wan >= 10 ? Math.round(wan) : wan.toFixed(1).replace(/\.0$/, '')}w`
  }
  if (count >= 1000) {
    const kilo = count / 1000
    return `${kilo.toFixed(1).replace(/\.0$/, '')}k`
  }
  return String(count)
}

// formatTime 输出相对时间，超过七天回落为日期。
function formatTime(value) {
  if (!value) return ''
  const then = new Date(value).getTime()
  if (Number.isNaN(then)) return ''
  const diff = Date.now() - then
  if (diff < 60 * 1000) return '刚刚'
  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / 60000)} 分钟前`
  if (diff < 24 * 60 * 60 * 1000) return `${Math.floor(diff / 3600000)} 小时前`
  if (diff < 7 * 24 * 60 * 60 * 1000) return `${Math.floor(diff / 86400000)} 天前`
  return new Date(then).toLocaleDateString()
}

export default function ClipCard({
  item,
  commentState,
  busy,
  onLike,
  onSave,
  onToggleComments,
  onSubmitComment,
}) {
  const [draft, setDraft] = useState('')
  const [brokenCover, setBrokenCover] = useState(false)
  const title = item.title || '未命名内容'
  const gradient = GRADIENTS[hashOf(item.clip_id) % GRADIENTS.length]
  const state = commentState || { open: false, items: [], loading: false, error: '' }

  async function handleSubmitComment(event) {
    event.preventDefault()
    const content = draft.trim()
    if (!content) return
    setDraft('')
    await onSubmitComment(item.clip_id, content)
  }

  return (
    <article className="pf-card">
      <div className="pf-cover">
        {item.cover_url && !brokenCover ? (
          <img
            src={item.cover_url}
            alt={`${title} 封面`}
            loading="lazy"
            decoding="async"
            onError={() => setBrokenCover(true)}
          />
        ) : (
          <span className="pf-cover-fallback" style={{ background: gradient }}>
            <span className="pf-cover-initial" aria-hidden="true">
              {title.slice(0, 1)}
            </span>
          </span>
        )}
        <span className="pf-cover-play" aria-hidden="true">
          <span>
            <Icon name="play" size={24} />
          </span>
        </span>
      </div>

      <div className="pf-card-body">
        <h3 className="pf-card-title" title={title}>
          {title}
        </h3>
        <div className="pf-creator">
          <Avatar name={`创作者 ${item.creator_id}`} size="sm" />
          <span className="pf-creator-name">创作者 {item.creator_id}</span>
          <span className="pf-creator-time">{formatTime(item.published_at)}</span>
        </div>

        <div className="pf-actions">
          <button
            type="button"
            className="pf-act"
            data-kind="like"
            aria-pressed={Boolean(item.liked)}
            aria-label={`点赞，当前 ${item.like_count} 次`}
            disabled={busy}
            onClick={() => onLike(item)}
          >
            <Icon name="heart" size={20} filled={Boolean(item.liked)} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.like_count)}</span>
          </button>
          <button
            type="button"
            className="pf-act"
            data-kind="save"
            aria-pressed={Boolean(item.saved)}
            aria-label={`收藏，当前 ${item.save_count} 次`}
            disabled={busy}
            onClick={() => onSave(item)}
          >
            <Icon name="bookmark" size={20} filled={Boolean(item.saved)} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.save_count)}</span>
          </button>
          <button
            type="button"
            className="pf-act"
            data-kind="comment"
            aria-expanded={Boolean(state.open)}
            aria-label="评论"
            onClick={() => onToggleComments(item.clip_id)}
          >
            <Icon name="comment" size={20} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.comment_count)}</span>
          </button>
          <span className="pf-act" data-kind="play" aria-label={`播放 ${item.play_count} 次`} role="text">
            <Icon name="play" size={20} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.play_count)}</span>
          </span>
        </div>
      </div>

      {state.open ? (
        <div className="pf-comments">
          {state.loading ? <p className="pf-comment-empty">评论加载中…</p> : null}
          {state.error ? (
            <p className="pf-comment-empty" role="alert">
              {state.error}
            </p>
          ) : null}
          {!state.loading && !state.error && (state.items || []).length === 0 ? (
            <p className="pf-comment-empty">还没有评论，来写第一条。</p>
          ) : null}
          {(state.items || []).length > 0 ? (
            <ul className="pf-comment-list">
              {state.items.map((comment) => (
                <li key={comment.id} className="pf-comment-item">
                  <span className="pf-comment-author">{comment.user_nickname || `用户 ${comment.user_id}`}</span>
                  {comment.content}
                </li>
              ))}
            </ul>
          ) : null}
          <form className="pf-comment-form" onSubmit={handleSubmitComment}>
            <input
              className="pf-comment-input"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              placeholder="写下你的评论"
              maxLength={200}
              aria-label="评论内容"
            />
            <button type="submit" className="pf-primary-btn" disabled={busy || !draft.trim()}>
              发送
            </button>
          </form>
        </div>
      ) : null}
    </article>
  )
}
