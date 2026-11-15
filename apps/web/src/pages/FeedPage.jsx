// FeedPage.jsx 组织场景切换、栅格、骨架屏、空态/错误态、分页与详情弹层。
// 场景与详情都由 URL 驱动：/feed?scene=hot 直达场景，/clip/:clipId?from=... 直达详情弹层。
// 各场景的时间线进度（items/cursor/hasMore）持久化到 sessionStorage：重新进入、刷新或
// 从详情返回时优先恢复，不再重复请求第一页；游标被服务端判无效时清掉坏数据回到首屏。
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
import useMediaPrefetch from '../hooks/useMediaPrefetch.js'
import { ApiError, api, newClientId } from '../lib/api.js'
import { clearFeedProgress, loadFeedProgress, saveFeedProgress } from '../lib/feedProgress.js'

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
// PAGE_SIZE 是首屏与续页统一的页大小。
const PAGE_SIZE = 10

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
    creator: null,
  }
}

export default function FeedPage({ onGoPublish, me, onOpenUser }) {
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
  const [loadingFirst, setLoadingFirst] = useState(true)
  const [firstError, setFirstError] = useState('')
  const [loadingMore, setLoadingMore] = useState(false)
  const [actionError, setActionError] = useState('')
  const [deepItem, setDeepItem] = useState(null)
  const [deepError, setDeepError] = useState('')
  const deepFetchRef = useRef('')
  const [playingId, setPlayingId] = useState(null)
  // 每次 Feed 请求生成一个 request_id，同批返回的卡片共用，供展示/播放事件按批关联。
  const requestIdRef = useRef(newClientId('feed'))
  // 首屏请求序号：场景切换后必须丢弃上一场景在途的首屏响应。
  const firstSeqRef = useRef(0)
  // 当前场景：异步续页返回时用它判断是否已经切走场景。
  const sceneRef = useRef(scene)
  // itemsScene 标记当前列表属于哪个场景，避免场景切换的过渡帧把旧列表写进新场景的持久化键。
  const [itemsScene, setItemsScene] = useState(scene)

  // loadFirstPage 拉取场景首屏；成功写入列表与游标，失败保留错误供 ErrorBanner 重试。
  const loadFirstPage = useCallback(async (targetScene) => {
    const seq = ++firstSeqRef.current
    setLoadingFirst(true)
    setFirstError('')
    try {
      const data = await api.feed({ scene: targetScene, limit: PAGE_SIZE })
      if (seq !== firstSeqRef.current) return
      const requestId = newClientId('feed')
      requestIdRef.current = requestId
      const nextItems = (data.items || []).map((item) => ({ ...item, request_id: requestId }))
      setItemsScene(targetScene)
      setItems(nextItems)
      setCursor(data.next_cursor || '')
      setHasMore(Boolean(data.has_more))
      // 空首屏不落盘，避免下次恢复时把「没有内容」固化；该场景的旧进度一并清掉。
      if (nextItems.length === 0) clearFeedProgress(targetScene)
    } catch (err) {
      if (seq !== firstSeqRef.current) return
      setFirstError(err && err.message ? err.message : '请求失败')
    } finally {
      if (seq === firstSeqRef.current) setLoadingFirst(false)
    }
  }, [])

  // 进入/切换场景：优先恢复该场景的持久化进度，恢复成功就不再请求第一页。
  useEffect(() => {
    sceneRef.current = scene
    firstSeqRef.current += 1 // 丢弃上一场景在途的首屏请求
    setPlayingId(null)
    setLoadingMore(false)
    const saved = loadFeedProgress(scene)
    if (saved) {
      const requestId = newClientId('feed')
      requestIdRef.current = requestId
      setItemsScene(scene)
      setItems(saved.items.map((item) => ({ ...item, request_id: requestId })))
      setCursor(saved.cursor)
      setHasMore(saved.hasMore)
      setLoadingFirst(false)
      setFirstError('')
      return
    }
    requestIdRef.current = newClientId('feed')
    setItemsScene(scene)
    setItems([])
    setCursor('')
    setHasMore(false)
    loadFirstPage(scene)
  }, [scene, loadFirstPage])

  // 进度落盘：只写当前场景自己的列表，空列表不写（首屏已单独清理）。
  useEffect(() => {
    if (itemsScene !== scene || items.length === 0) return
    saveFeedProgress(scene, { items, cursor, hasMore })
  }, [scene, itemsScene, items, cursor, hasMore])

  // 首屏（含恢复出来的列表）就绪后再预热预加载队列；失败静默，场景切换或离开页面时自动取消。
  const firstPageReady = items.length > 0 && !loadingFirst && !firstError
  useMediaPrefetch(firstPageReady, [scene, firstPageReady])

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

  // patchCreator 同步更新同一作者的所有卡片与详情，关注态在当前列表内立即一致。
  const patchCreator = useCallback((creatorId, following) => {
    const apply = (item) =>
      item && item.creator && item.creator.user_id === creatorId
        ? { ...item, creator: { ...item.creator, following } }
        : item
    setItems((prev) => prev.map(apply))
    setDeepItem((prev) => apply(prev))
  }, [])

  async function toggleFollow(item) {
    const creator = item.creator
    if (!creator || !creator.user_id) return
    if (creator.user_id === Number(me && me.user_id)) return
    const next = !creator.following
    patchCreator(creator.user_id, next)
    setActionError('')
    try {
      const data = await api.follow(creator.user_id, next)
      patchCreator(creator.user_id, data.following)
      // 关注流的可见性由关注关系驱动：关系变化后重新拉取首页，避免缓存里的旧集合。
      if (scene === 'following') loadFirstPage(scene)
    } catch (err) {
      patchCreator(creator.user_id, creator.following)
      setActionError(err.message)
    }
  }

  const openUser = useCallback(
    (userId) => {
      if (onOpenUser) onOpenUser(userId)
    },
    [onOpenUser],
  )

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
    const requestId = newClientId('feed')
    try {
      const next = await api.feed({ scene, cursor, limit: PAGE_SIZE })
      // 切场景后返回的续页结果不属于当前列表，直接丢弃。
      if (sceneRef.current !== scene) return
      setItems((prev) => {
        const seen = new Set(prev.map((item) => item.clip_id))
        const merged = [...prev]
        for (const item of next.items || []) {
          if (!seen.has(item.clip_id)) {
            seen.add(item.clip_id)
            merged.push({ ...item, request_id: requestId })
          }
        }
        return merged
      })
      setCursor(next.next_cursor || '')
      setHasMore(Boolean(next.has_more))
    } catch (err) {
      if (sceneRef.current !== scene) return
      // 服务端判定游标无效（400）说明持久化的进度已不可用：清掉坏数据回第一页，不把用户卡在错误态。
      if (err instanceof ApiError && err.code === 400) {
        clearFeedProgress(scene)
        setItems([])
        setCursor('')
        setHasMore(false)
        loadFirstPage(scene)
        return
      }
      setActionError(err.message)
    } finally {
      if (sceneRef.current === scene) setLoadingMore(false)
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
  const showSkeleton = loadingFirst && items.length === 0
  const showEmpty = !loadingFirst && !firstError && items.length === 0

  return (
    <div className="pf-feed-page">
      <h1 className="pf-sr-only">内容流</h1>
      <SceneTabs scenes={SCENES} scene={scene} onChange={handleScene} hint={SCENE_HINTS[scene]} />

      <ErrorBanner message={firstError} onRetry={() => loadFirstPage(scene)} />

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
              scene={scene}
              busy={false}
              onLike={toggleLike}
              onSave={toggleSave}
              onOpen={openDetail}
              suspended={clipId !== null || (playingId !== null && playingId !== item.clip_id)}
              onPlay={setPlayingId}
              viewerId={Number(me && me.user_id) || 0}
              onCreator={openUser}
              onFollow={toggleFollow}
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
          viewerId={Number(me && me.user_id) || 0}
          onCreator={openUser}
          onFollow={toggleFollow}
        />
      ) : null}
    </div>
  )
}
