// PublishPage.jsx 提供上传到创建内容的完整表单、素材预览与分阶段状态反馈。
import React, { useEffect, useState } from 'react'
import Icon from '../components/Icon.jsx'
import { api, FILE_KIND } from '../lib/api.js'
import { formatFileSize } from '../lib/format.js'

const STAGE_LABELS = {
  uploading: '正在上传素材…',
  cover: '正在上传封面…',
  creating: '正在创建内容…',
}

export default function PublishPage({ onPublished }) {
  const [title, setTitle] = useState('')
  const [file, setFile] = useState(null)
  const [cover, setCover] = useState(null)
  const [coverPreview, setCoverPreview] = useState('')
  const [stage, setStage] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (!cover) {
      setCoverPreview('')
      return undefined
    }
    const url = URL.createObjectURL(cover)
    setCoverPreview(url)
    return () => URL.revokeObjectURL(url)
  }, [cover])

  const submitting = Boolean(stage)

  async function handleSubmit(event) {
    event.preventDefault()
    setError('')
    setNotice('')
    if (!title.trim()) {
      setError('请填写标题')
      return
    }
    if (!file) {
      setError('请选择要上传的素材文件')
      return
    }

    try {
      setStage('uploading')
      const uploaded = await api.uploadFile(file, FILE_KIND)

      let coverUrl = ''
      if (cover) {
        setStage('cover')
        const uploadedCover = await api.uploadFile(cover, 'cover')
        coverUrl = uploadedCover.url
      }

      setStage('creating')
      await api.publishClip({ title: title.trim(), fileUrl: uploaded.url, coverUrl })
      setTitle('')
      setFile(null)
      setCover(null)
      setNotice('发布成功，内容已进入最新流。')
    } catch (err) {
      setError(err.message)
    } finally {
      setStage('')
    }
  }

  return (
    <section className="pf-form-page" aria-labelledby="pf-publish-title">
      <h1 id="pf-publish-title" className="pf-page-title">
        发布内容
      </h1>
      <p className="pf-page-hint">上传素材并补充标题；封面可留空，系统会以暗色首字占位。</p>

      <form className="pf-form-card" onSubmit={handleSubmit} noValidate>
        <div className="pf-field">
          <label htmlFor="pf-publish-title-input">标题</label>
          <input
            id="pf-publish-title-input"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="一句话说清这条内容"
            maxLength={120}
            disabled={submitting}
            required
          />
        </div>

        <div className="pf-field">
          <label htmlFor="pf-publish-file">素材文件</label>
          <label className={`pf-file ${file ? 'has-file' : ''}`} htmlFor="pf-publish-file">
            <input
              id="pf-publish-file"
              type="file"
              accept=".mp4,.mov,.webm"
              onChange={(event) => setFile(event.target.files?.[0] || null)}
              disabled={submitting}
            />
            <span className="pf-file-icon" aria-hidden="true">
              <Icon name="upload" size={20} />
            </span>
            <span className="pf-file-text">
              {file ? file.name : '选择素材文件'}
              <span className="pf-file-meta">{file ? formatFileSize(file.size) : 'mp4 / mov / webm'}</span>
            </span>
          </label>
        </div>

        <div className="pf-field">
          <label htmlFor="pf-publish-cover">封面（可选）</label>
          <label className={`pf-file ${cover ? 'has-file' : ''}`} htmlFor="pf-publish-cover">
            <input
              id="pf-publish-cover"
              type="file"
              accept=".jpg,.jpeg,.png,.webp"
              onChange={(event) => setCover(event.target.files?.[0] || null)}
              disabled={submitting}
            />
            <span className="pf-file-text">
              {cover ? cover.name : '选择封面图片'}
              <span className="pf-file-meta">{cover ? formatFileSize(cover.size) : 'jpg / png / webp'}</span>
            </span>
          </label>
          {coverPreview ? (
            <div className="pf-cover-preview">
              <img src={coverPreview} alt="封面预览" />
            </div>
          ) : null}
        </div>

        {error ? (
          <p className="pf-note error" role="alert">
            {error}
          </p>
        ) : null}

        {notice ? (
          <div className="pf-note success" role="status">
            <span>{notice}</span>
            <button type="button" className="pf-ghost-btn" onClick={onPublished}>
              去首页查看
            </button>
          </div>
        ) : null}

        {stage ? (
          <p className="pf-note" role="status" aria-live="polite">
            {STAGE_LABELS[stage]}
          </p>
        ) : null}

        <div className="pf-form-actions">
          <button type="submit" className="pf-primary-btn" disabled={submitting}>
            {stage ? STAGE_LABELS[stage] : '上传并发布'}
          </button>
        </div>
      </form>
    </section>
  )
}
