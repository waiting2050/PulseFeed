// FeedPage.jsx 组织场景切换、栅格、骨架屏、空态/错误态、分页与详情弹层。
// 场景与详情都由 URL 驱动：/feed?scene=hot 直达场景，/clip/:clipId?from=... 直达详情弹层。
import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useMatch, useNavigate } from 'react-router-dom'
import SceneTabs from '../components/SceneTabs.jsx'
import ClipCard from '../components/ClipCard.jsx'
import SkeletonCard from '../components/SkeletonCard.jsx'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import DetailSheet from '../components/DetailSheet.jsx'
import Spinner from '../components/Spinner.jsx'
import Toast from '../components/Toast.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'

const SCENES = [
  { key: 'latest', label: '最新' },
  { key: 'hot', label: '热门' },
  { key: 'following', label: '关注' },
  { key: 'for-you', label: '推荐' },
]

const SCENE_HINTS = {
  latest: '按发布时间倒序',
  hot: '按热度排序',
  following: '来自你关注的创作者',
  'for-you': '按兴趣推荐',
}

const SCENE_KEYS = SCENES.map((entry) => entry.key)
const DEFAULT_SCENE = 'latest'

// readScene 校验 URL 里的场景取值，非法值回落到默认场景。
function readScene(search) {
  const value = new URLSearchParams(search).get('scene')
  return SCENE_KEYS.includes(value) ? value : DEFAULT_SCENE
}

// readFrom 只接受站内绝对路径，并从中解析来源路径与来源场景。
function readFrom(raw) {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//')) return null
  const queryIndex = raw.indexOf('?')
  return {
    path: raw,
    scene: readScene(queryIndex === -1 ? '' : raw.slice(queryIndex)),
  }
}

// normalizeClip 把单条查询结果补齐为详情弹层所需的完整视图字段。
function normalizeClip(data, clipId) {
  return {
    clip_id: Number(data && data.clip_id) || Number(clipId),
    creator_id: data ? data.creator_id : 0,
    title: (data && data.title) || '',
    video_url: (data && data.video_url) || '',
    cover_url: (data && data.cover_url) || '',
    duration_ms: (data && data.duration_ms) || 0,
    published_at: data ? data.published_at : '',
    liked: Boolean(data && data.liked),
    saved: Boolean(data && data.saved),
    like_count: Number(data && data.like_count) || 0,
    save_count: Number(data && data.save_count) || 0,
    comment_count: Number(data && data.comment_count) || 0,
    play_count: Number(data && data.play_count) || 0,
  }
}

export default function FeedPage({ onGoPublish }) {
  const navigate = useNavigate()
  const location = useLocation()
  const clipMatch = useMatch('/clip/:clipId')
  const clipId = clipMatch ? clipMatch.params.clipId : null

  // 详情路由下场景从 from 还原，保证弹层背后的列表与打开前一致。
  const from = readFrom(new URLSearchParams(location.search).get('from'))
  const scene = clipId ? (from ? from.scene : DEFAULT_SCENE) : readScene(location.search)
  const backPath = from ? from.path : `/feed?scene=${scene}`

  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [actionError, setActionError] = useState('')
  const [deepItem, setDeepItem] = useState(null)
  const [deepError, setDeepError] = useState('')
  const deepFetchRef = useRef('')
  const [playingId, setPlayingId] = useState(null)

  const firstPage = useApi(() => api.feed({ scene, limit: 10 }), [scene])

  useEffect(() => {
    setItems([])
    setCursor('')
    setHasMore(false)
    setPlayingId(null)
  }, [scene])

  useEffect(() => {
    if (!firstPage.data) return
    setItems(firstPage.data.items || [])
    setCursor(firstPage.data.next_cursor || '')
    setHasMore(Boolean(firstPage.data.has_more))
  }, [firstPage.data])

  // 深链兜底：列表里已有该条就直接复用，否则每个 id 只按需拉取一次。
  useEffect(() => {
    if (!clipId) {
      deepFetchRef.current = ''
      setDeepItem(null)
      setDeepError('')
      return undefined
    }
    if (items.some((item) => String(item.clip_id) === clipId)) {
      setDeepError('')
      return undefined
    }
    if (deepFetchRef.current === clipId) return undefined
    deepFetchRef.current = clipId
    let alive = true
    setDeepError('')
    api
      .getClip(clipId)
      .then((data) => {
        if (alive) setDeepItem(normalizeClip(data, clipId))
      })
      .catch((err) => {
        if (alive) setDeepError(err.message)
      })
    return () => {
      alive = false
    }
  }, [clipId, items])

  const patchItem = useCallback((id, patch) => {
    setItems((prev) => prev.map((item) => (item.clip_id === id ? { ...item, ...patch } : item)))
    setDeepItem((prev) => (prev && prev.clip_id === id ? { ...prev, ...patch } : prev))
  }, [])

  async function toggleLike(item) {
    const liked = !item.liked
    patchItem(item.clip_id, {
      liked,
      like_count: Math.max(0, item.like_count + (liked ? 1 : -1)),
    })
    setActionError('')
    try {
      const data = await api.like(item.clip_id, liked)
      patchItem(item.clip_id, { liked: data.active, like_count: data.like_count })
    } catch (err) {
      patchItem(item.clip_id, { liked: item.liked, like_count: item.like_count })
      setActionError(err.message)
    }
  }

  async function toggleSave(item) {
    const saved = !item.saved
    patchItem(item.clip_id, {
      saved,
      save_count: Math.max(0, item.save_count + (saved ? 1 : -1)),
    })
    setActionError('')
    try {
      const data = await api.save(item.clip_id, saved)
      patchItem(item.clip_id, { saved: data.active, save_count: data.save_count })
    } catch (err) {
      patchItem(item.clip_id, { saved: item.saved, save_count: item.save_count })
      setActionError(err.message)
    }
  }

  async function loadMore() {
    if (!hasMore || loadingMore) return
    setLoadingMore(true)
    setActionError('')
    try {
      const next = await api.feed({ scene, cursor, limit: 10 })
      setItems((prev) => {
        const seen = new Set(prev.map((item) => item.clip_id))
        const merged = [...prev]
        for (const item of next.items || []) {
          if (!seen.has(item.clip_id)) {
            seen.add(item.clip_id)
            merged.push(item)
          }
        }
        return merged
      })
      setCursor(next.next_cursor || '')
      setHasMore(Boolean(next.has_more))
    } catch (err) {
      setActionError(err.message)
    } finally {
      setLoadingMore(false)
    }
  }

  // 切换场景写回 URL，前进后退与刷新都能回到同一场景。
  const handleScene = useCallback(
    (next) => {
      navigate(`/feed?scene=${next}`)
    },
    [navigate],
  )

  // 打开详情时暂停所有卡片播放器，并把详情地址写入 URL；from 记录来源列表。
  const openDetail = useCallback(
    (clip) => {
      setPlayingId(null)
      navigate(`/clip/${clip.clip_id}?from=${encodeURIComponent(`/feed?scene=${scene}`)}`)
    },
    [navigate, scene],
  )

  // 关闭弹层回到来源列表；直接粘贴链接进来的默认回最新场景。
  const closeDetail = useCallback(() => {
    navigate(backPath, { replace: true })
  }, [navigate, backPath])

  const handleCommentAdded = useCallback(
    (id, count) => {
      patchItem(id, { comment_count: count })
    },
    [patchItem],
  )

  const activeItem = clipId
    ? items.find((item) => String(item.clip_id) === clipId) || deepItem
    : null
  const showSkeleton = firstPage.loading && items.length === 0
  const showEmpty = !firstPage.loading && !firstPage.error && items.length === 0

  return (
    <div className="pf-feed-page">
      <h1 className="pf-sr-only">内容流</h1>
      <SceneTabs scenes={SCENES} scene={scene} onChange={handleScene} hint={SCENE_HINTS[scene]} />

      <ErrorBanner message={firstPage.error} onRetry={firstPage.retry} />

      {actionError ? <Toast message={actionError} tone="error" onDismiss={() => setActionError('')} /> : null}

      {clipId && deepError && !activeItem ? (
        <Toast
          message={deepError}
          tone="error"
          action={{ label: '回到内容流', onClick: closeDetail }}
          onDismiss={closeDetail}
        />
      ) : null}

      {showSkeleton ? (
        <section className="pf-grid" aria-busy="true" aria-label="内容加载中">
          {[0, 1, 2, 3, 4, 5].map((key) => (
            <SkeletonCard key={key} />
          ))}
          <span className="pf-sr-only" role="status">
            正在加载内容
          </span>
        </section>
      ) : null}

      {showEmpty ? (
        <EmptyState
          title="这个场景还没有内容"
          description="换个场景看看，或者发布第一条内容。"
          actionLabel="去发布"
          onAction={onGoPublish}
        />
      ) : null}

      {items.length > 0 ? (
        <section className="pf-grid" aria-label="内容列表">
          {items.map((item) => (
            <ClipCard
              key={item.clip_id}
              item={item}
              busy={false}
              onLike={toggleLike}
              onSave={toggleSave}
              onOpen={openDetail}
              suspended={clipId !== null || (playingId !== null && playingId !== item.clip_id)}
              onPlay={setPlayingId}
            />
          ))}
        </section>
      ) : null}

      {items.length > 0 ? (
        <footer>
          {hasMore ? (
            <div className="pf-load-more">
              <button type="button" className="pf-primary-btn" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? <Spinner label="正在加载更多内容" /> : null}
                {loadingMore ? '加载中…' : '加载更多'}
              </button>
            </div>
          ) : (
            <p className="pf-end">已经到底了</p>
          )}
        </footer>
      ) : null}

      {activeItem ? (
        <DetailSheet
          key={activeItem.clip_id}
          item={activeItem}
          onClose={closeDetail}
          onLike={toggleLike}
          onSave={toggleSave}
          onCommentAdded={handleCommentAdded}
        />
      ) : null}
    </div>
  )
}
