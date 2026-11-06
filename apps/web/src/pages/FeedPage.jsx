// FeedPage.jsx 组织场景切换、栅格、骨架屏、空态与错误态；数据仍由上层 App 通过现有 API 提供。
import React from 'react'
import SceneTabs from '../components/SceneTabs.jsx'
import ClipCard from '../components/ClipCard.jsx'
import SkeletonCard from '../components/SkeletonCard.jsx'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'

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

// 仅开发模式支持 ?state=loading|empty，用于本地预览骨架与空态；生产构建会消除该分支。
function readPreviewState() {
  if (!import.meta.env.DEV) return ''
  const value = new URLSearchParams(window.location.search).get('state')
  return value === 'loading' || value === 'empty' ? value : ''
}

export default function FeedPage({
  scene,
  onScene,
  items,
  loading,
  error,
  hasMore,
  onLoadMore,
  onRetry,
  comments,
  busy,
  onLike,
  onSave,
  onToggleComments,
  onSubmitComment,
  onGoPublish,
}) {
  const preview = readPreviewState()
  const forcedPlaceholder = preview === 'loading' || preview === 'empty'
  const visibleItems = forcedPlaceholder ? [] : items
  const showSkeleton = preview === 'loading' || (loading && visibleItems.length === 0)
  const showEmpty = !showSkeleton && !error && visibleItems.length === 0

  return (
    <div className="pf-feed-page">
      <h1 className="pf-sr-only">内容流</h1>
      <SceneTabs scenes={SCENES} scene={scene} onChange={onScene} hint={SCENE_HINTS[scene]} />

      <ErrorBanner message={error} onRetry={onRetry} />

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

      {visibleItems.length > 0 ? (
        <section className="pf-grid" aria-label="内容列表">
          {visibleItems.map((item) => (
            <ClipCard
              key={item.clip_id}
              item={item}
              commentState={comments[item.clip_id]}
              busy={busy}
              onLike={onLike}
              onSave={onSave}
              onToggleComments={onToggleComments}
              onSubmitComment={onSubmitComment}
            />
          ))}
        </section>
      ) : null}

      {visibleItems.length > 0 ? (
        <footer>
          {hasMore ? (
            <div className="pf-load-more">
              <button type="button" className="pf-primary-btn" onClick={onLoadMore} disabled={loading}>
                {loading ? '加载中…' : '加载更多'}
              </button>
            </div>
          ) : (
            <p className="pf-end">已经到底了</p>
          )}
        </footer>
      ) : null}
    </div>
  )
}
