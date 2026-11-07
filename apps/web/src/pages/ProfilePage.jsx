// ProfilePage.jsx 同时承载「我的主页」/me 与公开主页 /users/:userId：
// 资料展示与编辑、关注/取关、关注/粉丝数弹窗列表、作品栅格游标分页。
import React, { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Avatar from '../components/Avatar.jsx'
import ClipCover from '../components/ClipCover.jsx'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import Spinner from '../components/Spinner.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'

const CLIP_LIMIT = 12
const LIST_LIMIT = 20

// useDialogFocus 锁住页面滚动、聚焦弹层并在 Esc 时关闭，Tab 在弹层内循环。
function useDialogFocus(dialogRef, onClose) {
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  }, [onClose])

  useEffect(() => {
    const opener = document.activeElement
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'

    function handleKey(event) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onCloseRef.current()
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
  }, [dialogRef])
}

// Modal 是资料编辑与关注列表复用的弹层外壳。
function Modal({ title, onClose, children }) {
  const dialogRef = useRef(null)
  useDialogFocus(dialogRef, onClose)

  return (
    <div className="pf-sheet-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <div ref={dialogRef} className="pf-modal" role="dialog" aria-modal="true" aria-label={title}>
        <div className="pf-modal-head">
          <span className="pf-sheet-eyebrow">{title}</span>
          <button type="button" className="pf-icon-btn" onClick={onClose} aria-label={`关闭${title}`}>
            ×
          </button>
        </div>
        <div className="pf-modal-body">{children}</div>
      </div>
    </div>
  )
}

// EditProfileModal 编辑昵称、头像（复用 kind=avatar 上传）与简介，只提交发生变化的字段。
function EditProfileModal({ profile, onClose, onSaved }) {
  const [nickname, setNickname] = useState(profile.nickname || '')
  const [avatarUrl, setAvatarUrl] = useState(profile.avatar_url || '')
  const [bio, setBio] = useState(profile.bio || '')
  const [uploading, setUploading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const fileRef = useRef(null)

  async function handleAvatar(event) {
    const file = event.target.files && event.target.files[0]
    if (!file) return
    if (!file.type || !file.type.startsWith('image/')) {
      setError('头像仅支持图片文件（jpg/png/webp）')
      return
    }
    setUploading(true)
    setError('')
    try {
      const data = await api.uploadFile(file, 'avatar')
      setAvatarUrl(data.url)
    } catch (err) {
      setError(err.message)
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  async function handleSubmit(event) {
    event.preventDefault()
    if (saving || uploading) return
    const nextNickname = nickname.trim()
    if (!nextNickname) {
      setError('昵称不能为空')
      return
    }
    const patch = {}
    if (nextNickname !== (profile.nickname || '')) patch.nickname = nextNickname
    if (avatarUrl !== (profile.avatar_url || '')) patch.avatar_url = avatarUrl
    const nextBio = bio.trim()
    if (nextBio !== (profile.bio || '')) patch.bio = nextBio
    if (Object.keys(patch).length === 0) {
      onClose()
      return
    }

    setSaving(true)
    setError('')
    try {
      const data = await api.updateMe(patch)
      onSaved(data)
      onClose()
    } catch (err) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal title="编辑资料" onClose={onClose}>
      <form className="pf-profile-form" onSubmit={handleSubmit}>
        {error ? (
          <p className="pf-note error" role="alert">
            {error}
          </p>
        ) : null}

        <div className="pf-profile-avatar-row">
          <Avatar size="lg" name={nickname || '我'} src={avatarUrl} />
          <div className="pf-profile-avatar-actions">
            <label className="pf-primary-btn pf-profile-upload">
              {uploading ? '上传中…' : '更换头像'}
              <input
                ref={fileRef}
                type="file"
                accept="image/jpeg,image/png,image/webp"
                onChange={handleAvatar}
                disabled={uploading || saving}
              />
            </label>
            <span className="pf-field-hint">支持 jpg / png / webp，选择后立即上传</span>
          </div>
        </div>

        <div className="pf-field">
          <label htmlFor="pf-profile-nickname">昵称</label>
          <input
            id="pf-profile-nickname"
            value={nickname}
            onChange={(event) => setNickname(event.target.value)}
            maxLength={64}
            disabled={saving}
          />
        </div>
        <div className="pf-field">
          <label htmlFor="pf-profile-bio">简介</label>
          <textarea
            id="pf-profile-bio"
            className="pf-profile-bio-input"
            value={bio}
            onChange={(event) => setBio(event.target.value)}
            maxLength={255}
            rows={3}
            disabled={saving}
            placeholder="介绍一下你自己"
          />
        </div>

        <div className="pf-form-actions">
          <button type="button" className="pf-ghost-btn" onClick={onClose} disabled={saving}>
            取消
          </button>
          <button type="submit" className="pf-primary-btn" disabled={saving || uploading}>
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

// RelationListModal 以弹窗展示关注/粉丝列表：保留公开主页上下文，翻页复用同一游标契约。
function RelationListModal({ userId, kind, onOpenUser, onClose }) {
  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    setItems([])
    setCursor('')
    setHasMore(false)
    setLoading(true)
    setError('')
    const loader = kind === 'following' ? api.userFollowing : api.userFollowers
    loader(userId, { limit: LIST_LIMIT })
      .then((data) => {
        if (!alive) return
        setItems(data.items || [])
        setCursor(data.next_cursor || '')
        setHasMore(Boolean(data.has_more))
      })
      .catch((err) => {
        if (alive) setError(err.message)
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [userId, kind])

  async function loadMore() {
    if (!hasMore || loadingMore) return
    setLoadingMore(true)
    setError('')
    const loader = kind === 'following' ? api.userFollowing : api.userFollowers
    try {
      const data = await loader(userId, { limit: LIST_LIMIT, cursor })
      setItems((prev) => {
        const seen = new Set(prev.map((entry) => entry.user_id))
        const merged = [...prev]
        for (const entry of data.items || []) {
          if (!seen.has(entry.user_id)) {
            seen.add(entry.user_id)
            merged.push(entry)
          }
        }
        return merged
      })
      setCursor(data.next_cursor || '')
      setHasMore(Boolean(data.has_more))
    } catch (err) {
      setError(err.message)
    } finally {
      setLoadingMore(false)
    }
  }

  return (
    <Modal title={kind === 'following' ? '关注列表' : '粉丝列表'} onClose={onClose}>
      {loading ? (
        <p className="pf-profile-list-state" role="status">
          <Spinner label="正在加载列表" />
          加载中…
        </p>
      ) : null}
      {error ? (
        <p className="pf-note error" role="alert">
          {error}
        </p>
      ) : null}
      {!loading && !error && items.length === 0 ? (
        <p className="pf-profile-list-state">这里还没有人。</p>
      ) : null}
      {items.length > 0 ? (
        <ul className="pf-profile-list">
          {items.map((entry) => (
            <li key={entry.user_id}>
              <button type="button" className="pf-profile-person" onClick={() => onOpenUser(entry.user_id)}>
                <Avatar size="sm" name={entry.nickname || `用户 ${entry.user_id}`} src={entry.avatar_url} />
                <span className="pf-profile-person-main">
                  <span className="pf-profile-person-name">{entry.nickname || `用户 ${entry.user_id}`}</span>
                  {entry.bio ? <span className="pf-profile-person-bio">{entry.bio}</span> : null}
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      {hasMore ? (
        <div className="pf-load-more">
          <button type="button" className="pf-ghost-btn" onClick={loadMore} disabled={loadingMore}>
            {loadingMore ? '加载中…' : '加载更多'}
          </button>
        </div>
      ) : null}
    </Modal>
  )
}

export default function ProfilePage({ me, onMeChange }) {
  const navigate = useNavigate()
  const params = useParams()
  const routeRaw = params.userId
  const myId = Number(me && me.user_id) || 0
  const routeUserId = routeRaw ? Number(routeRaw) : 0
  const validRoute = !routeRaw || (Number.isInteger(routeUserId) && routeUserId > 0)
  const isSelf = !routeRaw || routeUserId === myId
  const targetId = isSelf ? myId : routeUserId

  const [clips, setClips] = useState([])
  const [cursor, setCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [clipsError, setClipsError] = useState('')
  const [editing, setEditing] = useState(false)
  const [listKind, setListKind] = useState('')
  const [actionError, setActionError] = useState('')
  const [followBusy, setFollowBusy] = useState(false)

  const profile = useApi(() => (isSelf ? api.me() : api.getUser(targetId)), [isSelf, targetId], {
    immediate: validRoute,
  })
  const stats = useApi(
    () => (validRoute && targetId ? api.relationStats(targetId) : Promise.resolve(null)),
    [validRoute, targetId],
    { immediate: validRoute },
  )
  const firstClips = useApi(
    () => {
      if (!validRoute || !targetId) return Promise.resolve(null)
      return isSelf ? api.myClips({ limit: CLIP_LIMIT }) : api.userClips(targetId, { limit: CLIP_LIMIT })
    },
    [validRoute, targetId, isSelf],
    { immediate: validRoute },
  )

  useEffect(() => {
    profile.setData(null)
    stats.setData(null)
    setClips([])
    setCursor('')
    setHasMore(false)
    setClipsError('')
    setEditing(false)
    setListKind('')
    setActionError('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetId, isSelf])

  useEffect(() => {
    if (!firstClips.data) return
    setClips(firstClips.data.items || [])
    setCursor(firstClips.data.next_cursor || '')
    setHasMore(Boolean(firstClips.data.has_more))
  }, [firstClips.data])

  async function loadMore() {
    if (!hasMore || loadingMore || !targetId) return
    setLoadingMore(true)
    setClipsError('')
    try {
      const data = isSelf
        ? await api.myClips({ limit: CLIP_LIMIT, cursor })
        : await api.userClips(targetId, { limit: CLIP_LIMIT, cursor })
      setClips((prev) => {
        const seen = new Set(prev.map((clip) => clip.clip_id))
        const merged = [...prev]
        for (const clip of data.items || []) {
          if (!seen.has(clip.clip_id)) {
            seen.add(clip.clip_id)
            merged.push(clip)
          }
        }
        return merged
      })
      setCursor(data.next_cursor || '')
      setHasMore(Boolean(data.has_more))
    } catch (err) {
      setClipsError(err.message)
    } finally {
      setLoadingMore(false)
    }
  }

  async function toggleFollow() {
    if (!stats.data || followBusy || isSelf) return
    const next = !stats.data.following
    const previous = stats.data
    setFollowBusy(true)
    setActionError('')
    stats.setData({
      ...previous,
      following: next,
      follower_count: Math.max(0, previous.follower_count + (next ? 1 : -1)),
    })
    try {
      const data = await api.follow(targetId, next)
      stats.setData((prev) => ({
        ...prev,
        following: data.following,
        follower_count: data.follower_count,
      }))
    } catch (err) {
      stats.setData(previous)
      setActionError(err.message)
    } finally {
      setFollowBusy(false)
    }
  }

  function openClip(clip) {
    const from = isSelf ? '/me' : `/users/${targetId}`
    navigate(`/clip/${clip.clip_id}?from=${encodeURIComponent(from)}`)
  }

  function openUser(userId) {
    setListKind('')
    if (userId === myId) navigate('/me')
    else navigate(`/users/${userId}`)
  }

  function handleSaved(data) {
    profile.setData(data)
    if (onMeChange) onMeChange(data)
  }

  if (!validRoute) {
    return (
      <div className="pf-feed-page">
        <EmptyState
          title="用户不存在"
          description="这个主页地址无效，返回内容流继续浏览。"
          actionLabel="回到内容流"
          onAction={() => navigate('/feed')}
        />
      </div>
    )
  }

  const data = profile.data || {}
  const displayName = data.nickname || (targetId ? `用户 ${targetId}` : '我')
  const displayBio = data.bio || '还没有填写简介'

  return (
    <div className="pf-profile-page">
      <h1 className="pf-sr-only">{isSelf ? '我的主页' : '用户主页'}</h1>

      <ErrorBanner message={profile.error} onRetry={profile.retry} />

      {profile.loading && !profile.data ? (
        <section className="pf-profile-head" aria-busy="true" aria-label="资料加载中">
          <span className="pf-avatar pf-avatar-lg pf-sk-avatar" />
          <div className="pf-profile-id">
            <span className="pf-sk-line" />
            <span className="pf-sk-line short" />
          </div>
        </section>
      ) : null}

      {profile.data ? (
        <section className="pf-profile-head" aria-label="用户资料">
          <Avatar size="lg" name={displayName} src={data.avatar_url} />
          <div className="pf-profile-id">
            <h2 className="pf-profile-name">{displayName}</h2>
            <p className="pf-profile-bio">{displayBio}</p>
            <div className="pf-profile-stats">
              <button
                type="button"
                className="pf-profile-stat"
                onClick={() => setListKind('following')}
                aria-label={`查看关注，共 ${(stats.data && stats.data.following_count) || 0} 人`}
              >
                <b className="pf-num">{(stats.data && stats.data.following_count) || 0}</b>
                <span>关注</span>
              </button>
              <button
                type="button"
                className="pf-profile-stat"
                onClick={() => setListKind('followers')}
                aria-label={`查看粉丝，共 ${(stats.data && stats.data.follower_count) || 0} 人`}
              >
                <b className="pf-num">{(stats.data && stats.data.follower_count) || 0}</b>
                <span>粉丝</span>
              </button>
            </div>
          </div>
          <div className="pf-profile-actions">
            {isSelf ? (
              <button type="button" className="pf-ghost-btn" onClick={() => setEditing(true)}>
                编辑资料
              </button>
            ) : (
              <button
                type="button"
                className="pf-follow-btn"
                aria-pressed={Boolean(stats.data && stats.data.following)}
                disabled={followBusy || !stats.data}
                onClick={toggleFollow}
              >
                {stats.data && stats.data.following ? '已关注' : '关注'}
              </button>
            )}
          </div>
        </section>
      ) : null}

      {actionError ? (
        <p className="pf-note error" role="alert">
          {actionError}
        </p>
      ) : null}

      {profile.data ? (
        <section className="pf-profile-clips" aria-labelledby="pf-profile-clips-title">
          <div className="pf-profile-section-head">
            <h2 id="pf-profile-clips-title">作品</h2>
            <span className="pf-page-hint">按发布时间倒序</span>
          </div>

          {firstClips.loading && clips.length === 0 ? (
            <p className="pf-profile-list-state" role="status">
              <Spinner label="正在加载作品" />
              加载中…
            </p>
          ) : null}

          <ErrorBanner message={firstClips.error} onRetry={firstClips.retry} />
          {clipsError ? <ErrorBanner message={clipsError} onRetry={loadMore} /> : null}

          {!firstClips.loading && !firstClips.error && clips.length === 0 ? (
            <p className="pf-profile-list-state">{isSelf ? '你还没有发布作品。' : '这个用户还没有发布作品。'}</p>
          ) : null}

          {clips.length > 0 ? (
            <div className="pf-grid pf-profile-grid">
              {clips.map((clip) => {
                const title = clip.title || '未命名内容'
                return (
                  <button
                    key={clip.clip_id}
                    type="button"
                    className="pf-profile-clip"
                    onClick={() => openClip(clip)}
                    aria-label={`查看作品：${title}`}
                  >
                    <ClipCover clipId={clip.clip_id} title={title} coverUrl={clip.cover_url} />
                    <span className="pf-profile-clip-title">{title}</span>
                  </button>
                )
              })}
            </div>
          ) : null}

          {hasMore ? (
            <div className="pf-load-more">
              <button type="button" className="pf-primary-btn" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? <Spinner label="正在加载更多作品" /> : null}
                {loadingMore ? '加载中…' : '加载更多'}
              </button>
            </div>
          ) : clips.length > 0 ? (
            <p className="pf-end">已经到底了</p>
          ) : null}
        </section>
      ) : null}

      {editing && profile.data ? (
        <EditProfileModal profile={profile.data} onClose={() => setEditing(false)} onSaved={handleSaved} />
      ) : null}

      {listKind && targetId ? (
        <RelationListModal
          userId={targetId}
          kind={listKind}
          onOpenUser={openUser}
          onClose={() => setListKind('')}
        />
      ) : null}
    </div>
  )
}
