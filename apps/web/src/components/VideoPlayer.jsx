// VideoPlayer.jsx 提供播放器：播放/暂停、静音、可拖动进度与全屏；样式沿用 tokens，不引入皮肤库。
// 播放期间按会话采集首帧耗时、卡顿次数、播放时长与完播状态，结束/卸载时用既有 QoS 接口上报一次。
import React, { useCallback, useEffect, useRef, useState } from 'react'
import Icon from './Icon.jsx'
import { api, getCachedPlaybackConfig, loadPlaybackConfig } from '../lib/api.js'

// newEventId 生成单个播放会话的幂等键；同一会话只上报一次，后端再按 event_id 去重。
function newEventId() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return `pf-play-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// formatClock 把秒数格式化为 m:ss / h:mm:ss。
function formatClock(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0:00'
  const total = Math.floor(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }
  return `${minutes}:${String(secs).padStart(2, '0')}`
}

// initialSession 描述一次播放会话的 QoS 采集状态；hasPlayed 为 false 时不上报，避免空会话污染数据。
function initialSession() {
  return {
    eventId: newEventId(),
    startedAt: performance.now(),
    firstFrameMs: -1,
    stallCount: 0,
    playedMs: 0,
    resumeAt: 0,
    completed: false,
    reported: false,
    settling: false,
    // null 表示尚未抽样；false 表示本会话未命中，不再随机、也不再上报。
    sampled: null,
    hasPlayed: false,
    // playReported 保证同一会话只上报一次 play 事件，暂停后恢复不再重复上报。
    playReported: false,
  }
}

export default function VideoPlayer({
  clipId,
  src,
  poster,
  title,
  variant = 'card',
  autoPlay = false,
  suspended = false,
  onPlayStart,
  onPlayReport,
  onPlayComplete,
  onError,
}) {
  const containerRef = useRef(null)
  const videoRef = useRef(null)
  const sessionRef = useRef(null)
  const [playing, setPlaying] = useState(false)
  const [muted, setMuted] = useState(false)
  const [current, setCurrent] = useState(0)
  const [duration, setDuration] = useState(0)
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [buffering, setBuffering] = useState(false)
  const [failed, setFailed] = useState(false)

  const ensureSession = useCallback(() => {
    if (!sessionRef.current) sessionRef.current = initialSession()
    return sessionRef.current
  }, [])

  // reportQoS 结算当前会话，按 qos_sample_rate 在会话级别抽样一次后上报；
  // 未命中只记录 sampled=false，不写 reported，避免把会话误标成已上报。
  const reportQoS = useCallback(() => {
    const session = sessionRef.current
    const video = videoRef.current
    if (!session || session.reported || session.settling || !session.hasPlayed) return
    const now = performance.now()
    const playedMs = session.playedMs + (session.resumeAt > 0 ? now - session.resumeAt : 0)
    const completed =
      session.completed ||
      Boolean(video && Number.isFinite(video.duration) && video.duration > 0 && video.currentTime >= video.duration - 0.5)

    const finish = (config) => {
      session.settling = false
      if (session.reported) return
      if (session.sampled === null) {
        const rate = Number.isFinite(config && config.qos_sample_rate) ? config.qos_sample_rate : 0
        session.sampled = Math.random() < rate
      }
      if (!session.sampled) return
      session.reported = true
      api
        .reportQoS({
          clip_id: clipId,
          event_id: session.eventId,
          latency_ms: Math.max(0, Math.round(session.firstFrameMs < 0 ? 0 : session.firstFrameMs)),
          stall_count: session.stallCount,
          completed,
          // 后端契约没有时长字段，播放时长放入自由文本 client_info（上限 256 字符）。
          client_info: JSON.stringify({ duration_ms: Math.max(0, Math.round(playedMs)) }),
        })
        .catch(() => {})
    }

    session.settling = true
    const cached = getCachedPlaybackConfig()
    if (cached) {
      finish(cached)
      return
    }
    loadPlaybackConfig()
      .then(finish)
      .catch(() => {
        session.settling = false
      })
  }, [clipId])

  // 卸载（卡片被替换、详情关闭、切换场景）时结算未完成的播放会话。
  useEffect(() => {
    return () => reportQoS()
  }, [reportQoS])

  // 同一时间只允许一个在播：被父级标记为挂起时立即暂停。
  useEffect(() => {
    if (suspended) videoRef.current?.pause()
  }, [suspended])

  // 滚出视口暂停；重新进入不自动续播，由用户主动恢复。
  useEffect(() => {
    const container = containerRef.current
    if (!container || typeof IntersectionObserver !== 'function') return undefined
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => !entry.isIntersecting)) videoRef.current?.pause()
      },
      { threshold: 0.35 },
    )
    observer.observe(container)
    return () => observer.disconnect()
  }, [])

  // 自动播放仅在调用方显式开启时尝试；被浏览器拦截时保持暂停，不报错。
  useEffect(() => {
    const video = videoRef.current
    if (!autoPlay || !video) return
    ensureSession()
    const attempt = video.play()
    if (attempt && typeof attempt.catch === 'function') attempt.catch(() => {})
  }, [autoPlay, ensureSession, src])

  useEffect(() => {
    function handleFullscreenChange() {
      setIsFullscreen(
        Boolean(document.fullscreenElement && containerRef.current && document.fullscreenElement === containerRef.current),
      )
    }
    document.addEventListener('fullscreenchange', handleFullscreenChange)
    return () => document.removeEventListener('fullscreenchange', handleFullscreenChange)
  }, [])

  function togglePlay() {
    const video = videoRef.current
    if (!video || failed) return
    if (video.paused) {
      ensureSession()
      const attempt = video.play()
      if (attempt && typeof attempt.catch === 'function') attempt.catch(() => {})
    } else {
      video.pause()
    }
  }

  async function toggleFullscreen() {
    const container = containerRef.current
    const video = videoRef.current
    if (document.fullscreenElement) {
      await document.exitFullscreen?.()
      return
    }
    if (container && typeof container.requestFullscreen === 'function') {
      try {
        await container.requestFullscreen()
        return
      } catch {
        // 某些平台只允许对 video 元素全屏，继续走下面的回退。
      }
    }
    if (video && typeof video.webkitEnterFullscreen === 'function') video.webkitEnterFullscreen()
  }

  function handleSeek(event) {
    const video = videoRef.current
    if (!video) return
    const next = Number(event.target.value)
    video.currentTime = next
    setCurrent(next)
  }

  function handlePlay() {
    const session = ensureSession()
    session.resumeAt = performance.now()
    onPlayStart?.()
    // 同一会话只在首次开播时上报 play，暂停后恢复不重复计数。
    if (!session.playReported) {
      session.playReported = true
      onPlayReport?.(session.eventId)
    }
  }

  function handlePlaying() {
    const session = ensureSession()
    if (session.firstFrameMs < 0) session.firstFrameMs = performance.now() - session.startedAt
    session.hasPlayed = true
    setPlaying(true)
    setBuffering(false)
  }

  function handlePause() {
    const session = sessionRef.current
    if (session && session.resumeAt > 0) {
      session.playedMs += performance.now() - session.resumeAt
      session.resumeAt = 0
    }
    setPlaying(false)
    setBuffering(false)
  }

  function handleEnded() {
    const session = sessionRef.current
    let playedMs = 0
    if (session) {
      if (session.resumeAt > 0) {
        session.playedMs += performance.now() - session.resumeAt
        session.resumeAt = 0
      }
      session.completed = true
      playedMs = session.playedMs
    }
    setPlaying(false)
    reportQoS()
    if (session) onPlayComplete?.(session.eventId, playedMs)
    // 完播已结算，后续再播按新会话采集。
    sessionRef.current = null
  }

  function handleWaiting() {
    const session = sessionRef.current
    if (session && session.hasPlayed && !session.completed) session.stallCount += 1
    if (session && session.hasPlayed) setBuffering(true)
  }

  function handleMediaError() {
    setFailed(true)
    setBuffering(false)
    onError?.()
  }

  if (failed) return null

  const hasDuration = Number.isFinite(duration) && duration > 0

  return (
    <div ref={containerRef} className={`pf-player pf-player-${variant}`}>
      <video
        ref={videoRef}
        className="pf-player-video"
        src={src}
        poster={poster || undefined}
        playsInline
        preload="metadata"
        onClick={togglePlay}
        onPlay={handlePlay}
        onPlaying={handlePlaying}
        onPause={handlePause}
        onEnded={handleEnded}
        onWaiting={handleWaiting}
        onCanPlay={() => setBuffering(false)}
        onLoadedMetadata={(event) => setDuration(event.currentTarget.duration || 0)}
        onDurationChange={(event) => setDuration(event.currentTarget.duration || 0)}
        onTimeUpdate={(event) => setCurrent(event.currentTarget.currentTime)}
        onVolumeChange={(event) => setMuted(event.currentTarget.muted)}
        onError={handleMediaError}
      />

      {!playing && !buffering ? (
        <button
          type="button"
          className="pf-player-center"
          onClick={togglePlay}
          aria-label={`播放：${title}`}
        >
          <Icon name="play" size={variant === 'sheet' ? 28 : 22} />
        </button>
      ) : null}

      {buffering ? (
        <span className="pf-player-buffering" role="status">
          缓冲中…
        </span>
      ) : null}

      <div className="pf-player-bar">
        <button type="button" className="pf-player-btn" onClick={togglePlay} aria-label={playing ? '暂停' : '播放'}>
          <Icon name={playing ? 'pause' : 'play'} size={18} />
        </button>
        {variant === 'sheet' ? (
          <span className="pf-player-time pf-num">
            {formatClock(current)} / {formatClock(duration)}
          </span>
        ) : null}
        <input
          type="range"
          className="pf-player-seek"
          min={0}
          max={hasDuration ? duration : 0}
          step={0.1}
          value={hasDuration ? Math.min(current, duration) : 0}
          onChange={handleSeek}
          disabled={!hasDuration}
          aria-label="播放进度"
          aria-valuetext={`${formatClock(current)} / ${formatClock(duration)}`}
        />
        <button
          type="button"
          className="pf-player-btn"
          onClick={() => {
            const video = videoRef.current
            if (video) video.muted = !video.muted
          }}
          aria-label={muted ? '取消静音' : '静音'}
          aria-pressed={muted}
        >
          <Icon name={muted ? 'volumeOff' : 'volume'} size={18} />
        </button>
        <button
          type="button"
          className="pf-player-btn"
          onClick={toggleFullscreen}
          aria-label={isFullscreen ? '退出全屏' : '进入全屏'}
          aria-pressed={isFullscreen}
        >
          <Icon name="expand" size={18} />
        </button>
      </div>
    </div>
  )
}
