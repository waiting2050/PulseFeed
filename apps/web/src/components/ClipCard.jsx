// ClipCard.jsx 渲染 Feed 卡片：封面（含时长角标与悬停预览）、标题、作者行与操作栏。
// 点击封面或标题进入详情弹层；悬停预览仅在支持 hover 的设备且有真实 video_url 时启用。
import React, { useEffect, useRef, useState } from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'
import ClipCover from './ClipCover.jsx'
import { formatCount, formatDuration, formatTime } from '../lib/format.js'

// HOVER_PREVIEW 在模块加载时判定一次，触屏设备不注册预览，避免误触后自动播放。
const HOVER_PREVIEW =
  typeof window !== 'undefined' &&
  typeof window.matchMedia === 'function' &&
  window.matchMedia('(hover: hover) and (pointer: fine)').matches

export default function ClipCard({ item, busy, onLike, onSave, onOpen }) {
  const title = item.title || '未命名内容'
  const duration = formatDuration(item.duration_ms)
  const canPreview = HOVER_PREVIEW && Boolean(item.video_url)
  const [previewing, setPreviewing] = useState(false)
  const [previewFailed, setPreviewFailed] = useState(false)
  const previewRef = useRef(null)

  // 开始预览才挂载 video；播放被拒或资源异常时立即停止并标记失败，后续悬停不再重试。
  useEffect(() => {
    const video = previewRef.current
    if (!previewing || !video) return
    video.currentTime = 0
    const attempt = video.play()
    if (attempt && typeof attempt.catch === 'function') {
      attempt.catch(() => {
        setPreviewing(false)
        setPreviewFailed(true)
      })
    }
  }, [previewing])

  function startPreview() {
    if (canPreview && !previewFailed) setPreviewing(true)
  }

  function stopPreview() {
    const video = previewRef.current
    if (video) {
      video.pause()
      video.currentTime = 0
    }
    setPreviewing(false)
  }

  return (
    <article className="pf-card">
      <button
        type="button"
        className="pf-card-open"
        onClick={() => onOpen(item)}
        onPointerEnter={startPreview}
        onPointerLeave={stopPreview}
        onPointerCancel={stopPreview}
      >
        <ClipCover clipId={item.clip_id} title={title} coverUrl={item.cover_url}>
          {previewing ? (
            <video
              ref={previewRef}
              className="pf-cover-preview-video"
              src={item.video_url}
              muted
              playsInline
              preload="none"
              aria-hidden="true"
              tabIndex={-1}
              onError={() => {
                setPreviewing(false)
                setPreviewFailed(true)
              }}
            />
          ) : null}
          {!previewing ? (
            <span className="pf-cover-play" aria-hidden="true">
              <span>
                <Icon name="play" size={24} />
              </span>
            </span>
          ) : null}
          {duration ? (
            <span className="pf-cover-badge pf-cover-duration pf-num">{duration}</span>
          ) : null}
        </ClipCover>
        <span className="pf-sr-only">查看详情：{title}</span>
      </button>

      <div className="pf-card-body">
        <h3 className="pf-card-title" title={title}>
          <button type="button" className="pf-card-title-btn" onClick={() => onOpen(item)}>
            {title}
          </button>
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
            aria-label={`查看评论，当前 ${item.comment_count} 条`}
            onClick={() => onOpen(item)}
          >
            <Icon name="comment" size={20} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.comment_count)}</span>
          </button>
          <span className="pf-act" data-kind="play" role="text" aria-label={`播放 ${item.play_count} 次`}>
            <Icon name="play" size={20} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.play_count)}</span>
          </span>
        </div>
      </div>
    </article>
  )
}
