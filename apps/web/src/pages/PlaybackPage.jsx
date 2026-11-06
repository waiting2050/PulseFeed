// PlaybackPage.jsx 读取并保存播放配置，同时展示当前配置下的预加载队列。
import React, { useEffect, useState } from 'react'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import useApi from '../hooks/useApi.js'
import { api } from '../lib/api.js'

export default function PlaybackPage() {
  const config = useApi(() => api.playbackConfig(), [])
  const preload = useApi(() => api.preloadClips(), [])
  const [form, setForm] = useState({ preload_count: '', qos_sample_rate: '' })
  const [saving, setSaving] = useState(false)
  const [notice, setNotice] = useState('')
  const [actionError, setActionError] = useState('')

  useEffect(() => {
    if (!config.data) return
    setForm({
      preload_count: String(config.data.preload_count),
      qos_sample_rate: String(config.data.qos_sample_rate),
    })
  }, [config.data])

  function updateField(key, value) {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  async function handleSubmit(event) {
    event.preventDefault()
    setActionError('')
    setNotice('')

    const preloadCount = Number(form.preload_count)
    const sampleRate = Number(form.qos_sample_rate)
    if (!Number.isInteger(preloadCount) || preloadCount < 1 || preloadCount > 32) {
      setActionError('预加载条数需为 1–32 的整数')
      return
    }
    if (Number.isNaN(sampleRate) || sampleRate < 0.01 || sampleRate > 1) {
      setActionError('QoS 采样率需在 0.01–1 之间')
      return
    }

    setSaving(true)
    try {
      const data = await api.savePlaybackConfig({
        preload_count: preloadCount,
        qos_sample_rate: sampleRate,
      })
      setForm({
        preload_count: String(data.preload_count),
        qos_sample_rate: String(data.qos_sample_rate),
      })
      setNotice('配置已保存')
      preload.retry()
    } catch (err) {
      setActionError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const preloadIds = (preload.data && preload.data.clip_ids) || []

  return (
    <section className="pf-form-page" aria-labelledby="pf-playback-title">
      <h1 id="pf-playback-title" className="pf-page-title">
        播放配置
      </h1>
      <p className="pf-page-hint">控制预加载条数与客户端 QoS 采样率，保存后立即生效。</p>

      <ErrorBanner message={config.error} onRetry={config.retry} />

      <form className="pf-form-card" onSubmit={handleSubmit} noValidate>
        <div className="pf-form-grid">
          <div className="pf-field">
            <label htmlFor="pf-preload-count">预加载条数</label>
            <input
              id="pf-preload-count"
              type="number"
              inputMode="numeric"
              min="1"
              max="32"
              step="1"
              value={form.preload_count}
              onChange={(event) => updateField('preload_count', event.target.value)}
              disabled={saving || config.loading}
              required
            />
            <span className="pf-field-hint">范围 1–32，默认 8。</span>
          </div>
          <div className="pf-field">
            <label htmlFor="pf-qos-rate">QoS 采样率</label>
            <input
              id="pf-qos-rate"
              type="number"
              inputMode="decimal"
              min="0.01"
              max="1"
              step="0.01"
              value={form.qos_sample_rate}
              onChange={(event) => updateField('qos_sample_rate', event.target.value)}
              disabled={saving || config.loading}
              required
            />
            <span className="pf-field-hint">范围 0.01–1，默认 0.1。</span>
          </div>
        </div>

        {actionError ? (
          <p className="pf-note error" role="alert">
            {actionError}
          </p>
        ) : null}

        {notice ? (
          <p className="pf-note success" role="status">
            {notice}
          </p>
        ) : null}

        <div className="pf-form-actions">
          <button type="submit" className="pf-primary-btn" disabled={saving || config.loading}>
            {saving ? '保存中…' : '保存配置'}
          </button>
        </div>
      </form>

      <section className="pf-subsection" aria-labelledby="pf-preload-title">
        <div className="pf-page-head">
          <div>
            <h2 id="pf-preload-title" className="pf-subsection-title">
              预加载队列
            </h2>
            <p className="pf-page-hint">
              当前配置命中 <span className="pf-num">{preloadIds.length}</span> 条内容
            </p>
          </div>
          <button type="button" className="pf-ghost-btn" onClick={preload.retry} disabled={preload.loading}>
            {preload.loading ? '加载中…' : '刷新列表'}
          </button>
        </div>

        {preload.error ? (
          <ErrorBanner message={preload.error} onRetry={preload.retry} />
        ) : null}

        {!preload.error && preload.loading ? (
          <div className="pf-chip-list" aria-busy="true" aria-label="预加载列表加载中">
            {[0, 1, 2].map((key) => (
              <span key={key} className="pf-chip pf-chip-skeleton" />
            ))}
          </div>
        ) : null}

        {!preload.error && !preload.loading && preloadIds.length === 0 ? (
          <EmptyState
            icon="sliders"
            title="队列为空"
            description="调整预加载条数后刷新，或等待新内容发布。"
          />
        ) : null}

        {!preload.error && preloadIds.length > 0 ? (
          <ul className="pf-chip-list" aria-label="预加载内容编号">
            {preloadIds.map((clipId) => (
              <li key={clipId} className="pf-chip">
                <span className="pf-chip-label">内容</span>
                <span className="pf-num">{clipId}</span>
              </li>
            ))}
          </ul>
        ) : null}
      </section>
    </section>
  )
}
