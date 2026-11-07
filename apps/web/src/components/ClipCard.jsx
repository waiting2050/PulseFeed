// ClipCard.jsx 渲染 Feed 卡片：有 video_url 时展示内联播放器，否则保持封面 + 首字兜底。
// 卡片进入视口上报 exposed、开始播放上报 play、播完上报 complete、未播放即划走上报 skip；
// 展示与划走按 (request_id, clip_id) 去重，且只在登录态上报。
import React, { useEffect, useRef, useState } from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'
import ClipCover from './ClipCover.jsx'
import VideoPlayer from './VideoPlayer.jsx'
import { api, getToken } from '../lib/api.js'
import { formatCount, formatDuration, formatTime } from '../lib/format.js'

// reportedEvents 记录已上报的展示/划走事件，避免同一批请求内重复上报；
// 卡片卸载重挂后同一 request_id 仍不会重复发送。
const reportedEvents = new Set()

export default function ClipCard({
  item,
  scene,
  busy,
  onLike,
  onSave,
  onOpen,
  suspended = false,
  onPlay,
  viewerId = 0,
  onCreator,
  onFollow,
}) {
  const title = item.title || '未命名内容'
  const creator = item.creator || null
  const creatorId = (creator && creator.user_id) || item.creator_id
  const creatorName = (creator && creator.nickname) || `创作者 ${item.creator_id}`
  const canFollow = Boolean(creator && creatorId && creatorId !== viewerId && onFollow)
  const duration = formatDuration(item.duration_ms)
  const [videoFailed, setVideoFailed] = useState(false)
  const hasVideo = Boolean(item.video_url) && !videoFailed
  const cardRef = useRef(null)
  const exposedRef = useRef(false)
  const playedRef = useRef(false)
  const requestId = item.request_id || ''

  // reportEvent 统一发送展示/播放事件；未登录、缺少批次或场景时直接跳过。
  function reportEvent(eventType, eventId, watchMs = 0, completed = false) {
    if (!requestId || !scene || !getToken()) return
    api
      .reportPlayEvent({
        clipId: item.clip_id,
        eventId,
        eventType,
        scene,
        requestId,
        watchMs,
        completed,
      })
      .catch(() => {})
  }

  // 卡片至少半屏可见时上报一次 exposed，同一 request 内不重复。
  useEffect(() => {
    const node = cardRef.current
    if (!node || !requestId || !scene || !getToken()) return undefined
    if (typeof IntersectionObserver !== 'function') return undefined
    const exposureKey = `${requestId}:${item.clip_id}:exposed`
    const skipKey = `${requestId}:${item.clip_id}:skip`
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting && entry.intersectionRatio >= 0.5) {
            if (exposedRef.current || reportedEvents.has(exposureKey)) continue
            exposedRef.current = true
            reportedEvents.add(exposureKey)
            reportEvent('exposed', exposureKey)
            continue
          }
          // 展示过但从未开始播放就被划走：记一次 skip，不计入播放。
          if (!entry.isIntersecting && exposedRef.current && !playedRef.current && !reportedEvents.has(skipKey)) {
            reportedEvents.add(skipKey)
            reportEvent('skip', skipKey)
          }
        }
      },
      { threshold: [0, 0.5] },
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [requestId, scene, item.clip_id])

  // 开播上报 play；同一播放会话只在首次开播时触发，由播放器保证。
  function handlePlayReport(sessionEventId) {
    playedRef.current = true
    reportEvent('play', sessionEventId)
  }

  // 播完上报 complete，watch_ms 取本会话累计播放时长。
  function handlePlayComplete(sessionEventId, playedMs) {
    playedRef.current = true
    reportEvent('complete', `${sessionEventId}:complete`, playedMs, true)
  }

  return (
    <article ref={cardRef} className="pf-card">
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
              onPlayReport={handlePlayReport}
              onPlayComplete={handlePlayComplete}
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
          <Avatar name={creatorName} src={creator && creator.avatar_url} size="sm" />
          {onCreator ? (
            <button
              type="button"
              className="pf-creator-name pf-creator-link"
              onClick={() => onCreator(creatorId)}
              aria-label={`查看作者主页：${creatorName}`}
            >
              {creatorName}
            </button>
          ) : (
            <span className="pf-creator-name">{creatorName}</span>
          )}
          {canFollow ? (
            <button
              type="button"
              className="pf-follow-btn pf-follow-btn-sm"
              aria-pressed={Boolean(creator.following)}
              onClick={() => onFollow(item)}
              aria-label={`${creator.following ? '取消关注' : '关注'}${creatorName}`}
            >
              {creator.following ? '已关注' : '关注'}
            </button>
          ) : null}
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
