// ClipCard.jsx 渲染 Feed 卡片：有 video_url 时展示内联播放器，否则保持封面 + 首字兜底。
// 播放器负责播放/暂停、静音、进度与全屏；标题与「详情」按钮进入详情弹层。
import React, { useState } from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'
import ClipCover from './ClipCover.jsx'
import VideoPlayer from './VideoPlayer.jsx'
import { formatCount, formatDuration, formatTime } from '../lib/format.js'

export default function ClipCard({ item, busy, onLike, onSave, onOpen, suspended = false, onPlay }) {
  const title = item.title || '未命名内容'
  const duration = formatDuration(item.duration_ms)
  const [videoFailed, setVideoFailed] = useState(false)
  const hasVideo = Boolean(item.video_url) && !videoFailed

  return (
    <article className="pf-card">
      <div className="pf-card-media">
        <ClipCover clipId={item.clip_id} title={title} coverUrl={item.cover_url}>
          {hasVideo ? (
            <VideoPlayer
              clipId={item.clip_id}
              src={item.video_url}
              poster={item.cover_url}
              title={title}
              variant="card"
              suspended={suspended}
              onPlayStart={() => onPlay?.(item.clip_id)}
              onError={() => setVideoFailed(true)}
            />
          ) : null}
          {!hasVideo ? (
            <button
              type="button"
              className="pf-card-open"
              onClick={() => onOpen(item)}
              aria-label={`查看详情：${title}`}
            >
              <span className="pf-cover-play" aria-hidden="true">
                <span>
                  <Icon name="play" size={24} />
                </span>
              </span>
            </button>
          ) : null}
          {duration ? (
            <span className={`pf-cover-badge ${hasVideo ? '' : 'pf-cover-duration'} pf-num`.trim()}>
              {duration}
            </span>
          ) : null}
        </ClipCover>
        {hasVideo ? (
          <button
            type="button"
            className="pf-card-detail"
            onClick={() => onOpen(item)}
            aria-label={`查看详情：${title}`}
          >
            详情
          </button>
        ) : null}
      </div>

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
          <span className="pf-act" data-kind="play" aria-label={`播放 ${item.play_count} 次`}>
            <Icon name="play" size={20} className="pf-act-icon" />
            <span className="pf-act-count">{formatCount(item.play_count)}</span>
          </span>
        </div>
      </div>
    </article>
  )
}
