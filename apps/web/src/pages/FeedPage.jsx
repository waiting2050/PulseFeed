// FeedPage.jsx 组织场景切换、栅格、骨架屏、空态/错误态、分页与详情弹层。
import React, { useCallback, useEffect, useState } from 'react'
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

export default function FeedPage({ scene, onScene, onGoPublish }) {
  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [actionError, setActionError] = useState('')
  const [activeId, setActiveId] = useState(null)
  const [playingId, setPlayingId] = useState(null)

  const firstPage = useApi(() => api.feed({ scene, limit: 10 }), [scene])

  useEffect(() => {
    setItems([])
    setCursor('')
    setHasMore(false)
    setActiveId(null)
    setPlayingId(null)
  }, [scene])

  useEffect(() => {
    if (!firstPage.data) return
    setItems(firstPage.data.items || [])
    setCursor(firstPage.data.next_cursor || '')
    setHasMore(Boolean(firstPage.data.has_more))
  }, [firstPage.data])

  const patchItem = useCallback((clipId, patch) => {
    setItems((prev) => prev.map((item) => (item.clip_id === clipId ? { ...item, ...patch } : item)))
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

  // 打开详情时暂停所有卡片播放器，避免弹层与卡片同时出声。
  const openDetail = useCallback((clip) => {
    setPlayingId(null)
    setActiveId(clip.clip_id)
  }, [])
  const closeDetail = useCallback(() => setActiveId(null), [])
  const handleCommentAdded = useCallback(
    (clipId, count) => {
      patchItem(clipId, { comment_count: count })
    },
    [patchItem],
  )

  const activeItem = items.find((item) => item.clip_id === activeId)
  const showSkeleton = firstPage.loading && items.length === 0
  const showEmpty = !firstPage.loading && !firstPage.error && items.length === 0

  return (
    <div className="pf-feed-page">
      <h1 className="pf-sr-only">内容流</h1>
      <SceneTabs scenes={SCENES} scene={scene} onChange={onScene} hint={SCENE_HINTS[scene]} />

      <ErrorBanner message={firstPage.error} onRetry={firstPage.retry} />

      {actionError ? <Toast message={actionError} tone="error" onDismiss={() => setActionError('')} /> : null}

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
              suspended={activeId !== null || (playingId !== null && playingId !== item.clip_id)}
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
