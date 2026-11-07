// DetailSheet.jsx 渲染内容详情弹层/移动端 Sheet：封面与播放、操作栏、评论列表与提交。
import React, { useCallback, useEffect, useRef, useState } from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'
import ClipCover from './ClipCover.jsx'
import Spinner from './Spinner.jsx'
import VideoPlayer from './VideoPlayer.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'
import { formatCount, formatTime } from '../lib/format.js'

export default function DetailSheet({ item, onClose, onLike, onSave, onCommentAdded }) {
  const dialogRef = useRef(null)
  const closeRef = useRef(null)
  const [mediaError, setMediaError] = useState('')
  const [draft, setDraft] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const [added, setAdded] = useState([])

  const comments = useApi(() => api.comments(item.clip_id, { limit: 20 }), [item.clip_id])

  useEffect(() => {
    setMediaError('')
    setAdded([])
    setDraft('')
    setSubmitError('')
  }, [item.clip_id])

  // 打开时锁定页面滚动、把焦点移入弹层，Esc 关闭并把焦点还给触发元素。
  useEffect(() => {
    const opener = document.activeElement
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    closeRef.current?.focus()

    function handleKey(event) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab' || !dialogRef.current) return
      const focusable = dialogRef.current.querySelectorAll(
        'button, input, textarea, select, a[href], [tabindex]:not([tabindex="-1"])',
      )
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('keydown', handleKey)
      document.body.style.overflow = previousOverflow
      if (opener && typeof opener.focus === 'function') opener.focus()
    }
  }, [onClose])

  const handleSubmit = useCallback(
    async (event) => {
      event.preventDefault()
      const content = draft.trim()
      if (!content || submitting) return
      setSubmitting(true)
      setSubmitError('')
      try {
        const created = await api.createComment(item.clip_id, content)
        setAdded((prev) => [created, ...prev])
        setDraft('')
        onCommentAdded(item.clip_id, created.comment_count)
      } catch (err) {
        setSubmitError(err.message)
      } finally {
        setSubmitting(false)
      }
    },
    [draft, submitting, item.clip_id, onCommentAdded],
  )

  const commentItems = [...added, ...((comments.data && comments.data.items) || [])]
  const title = item.title || '未命名内容'

  return (
    <div className="pf-sheet-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <div
        ref={dialogRef}
        className="pf-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="pf-sheet-title"
      >
        <div className="pf-sheet-head">
          <span className="pf-sheet-eyebrow">内容详情</span>
          <button ref={closeRef} type="button" className="pf-icon-btn" onClick={onClose} aria-label="关闭详情">
            <Icon name="close" size={20} />
          </button>
        </div>

        <div className="pf-sheet-body">
          <div className="pf-sheet-media">
            <ClipCover clipId={item.clip_id} title={title} coverUrl={item.cover_url}>
              {item.video_url ? (
                <VideoPlayer
                  clipId={item.clip_id}
                  src={item.video_url}
                  poster={item.cover_url}
                  title={title}
                  variant="sheet"
                  onError={() => setMediaError('素材暂时无法播放，已回退到封面。')}
                />
              ) : null}
            </ClipCover>
            {mediaError ? (
              <p className="pf-note" role="status">
                {mediaError}
              </p>
            ) : null}
          </div>

          <div className="pf-sheet-info">
            <h2 id="pf-sheet-title" className="pf-sheet-title">
              {title}
            </h2>
            <div className="pf-creator">
              <Avatar name={`创作者 ${item.creator_id}`} size="sm" />
              <span className="pf-creator-name">创作者 {item.creator_id}</span>
              <span className="pf-creator-time">{formatTime(item.published_at)}</span>
            </div>

            <div className="pf-actions pf-sheet-actions">
              <button
                type="button"
                className="pf-act"
                data-kind="like"
                aria-pressed={Boolean(item.liked)}
                aria-label={`点赞，当前 ${item.like_count} 次`}
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
                onClick={() => onSave(item)}
              >
                <Icon name="bookmark" size={20} filled={Boolean(item.saved)} className="pf-act-icon" />
                <span className="pf-act-count">{formatCount(item.save_count)}</span>
              </button>
              <span className="pf-act" data-kind="play" role="text" aria-label={`播放 ${item.play_count} 次`}>
                <Icon name="play" size={20} className="pf-act-icon" />
                <span className="pf-act-count">{formatCount(item.play_count)}</span>
              </span>
            </div>

            <section className="pf-comment-section" aria-labelledby="pf-comment-heading">
              <div className="pf-comment-section-head">
                <h3 id="pf-comment-heading">评论</h3>
                <span className="pf-num">{formatCount(item.comment_count)}</span>
              </div>

              <div className="pf-comment-scroll">
                {comments.loading ? (
                  <p className="pf-comment-empty" role="status">
                    评论加载中…
                  </p>
                ) : null}
                {comments.error ? (
                  <div className="pf-comment-error" role="alert">
                    <span>{comments.error}</span>
                    <button type="button" className="pf-error-retry" onClick={comments.retry}>
                      重试
                    </button>
                  </div>
                ) : null}
                {!comments.loading && !comments.error && commentItems.length === 0 ? (
                  <p className="pf-comment-empty">还没有评论，来写第一条。</p>
                ) : null}
                {commentItems.length > 0 ? (
                  <ul className="pf-comment-list">
                    {commentItems.map((comment) => (
                      <li key={comment.id} className="pf-comment-item">
                        <span className="pf-comment-author">{comment.user_nickname || `用户 ${comment.user_id}`}</span>
                        <span className="pf-comment-text">{comment.content}</span>
                        <span className="pf-comment-time">{formatTime(comment.created_at)}</span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </div>

              <form className="pf-comment-form" onSubmit={handleSubmit}>
                <label className="pf-sr-only" htmlFor="pf-comment-input">
                  评论内容
                </label>
                <input
                  id="pf-comment-input"
                  className="pf-comment-input"
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  placeholder="写下你的评论"
                  maxLength={1000}
                  disabled={submitting}
                />
                <button type="submit" className="pf-primary-btn" disabled={submitting || !draft.trim()}>
                  {submitting ? <Spinner label="正在发送评论" /> : null}
                  {submitting ? '发送中…' : '发送'}
                </button>
              </form>
              {submitError ? (
                <p className="pf-note error" role="alert">
                  {submitError}
                </p>
              ) : null}
            </section>
          </div>
        </div>
      </div>
    </div>
  )
}
