// PlaybackPage.jsx 读取并保存播放配置，同时展示当前配置下的预加载队列。
import React, { useEffect, useState } from 'react'
import EmptyState from '../components/EmptyState.jsx'
import ErrorBanner from '../components/ErrorBanner.jsx'
import Spinner from '../components/Spinner.jsx'
import Toast from '../components/Toast.jsx'
import useApi from '../hooks/useApi.js'
import { api, primePlaybackConfig } from '../lib/api.js'

export default function PlaybackPage() {
  const config = useApi(() => api.playbackConfig(), [])
  const preload = useApi(() => api.preloadClips(), [])
  const [form, setForm] = useState({ preload_count: '', qos_sample_rate: '', buffer_ms: '' })
  const [saving, setSaving] = useState(false)
  const [feedback, setFeedback] = useState(null)

  useEffect(() => {
    if (!config.data) return
    primePlaybackConfig(config.data)
    setForm({
      preload_count: String(config.data.preload_count),
      qos_sample_rate: String(config.data.qos_sample_rate),
      buffer_ms: String(config.data.buffer_ms),
    })
  }, [config.data])

  function updateField(key, value) {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  async function handleSubmit(event) {
    event.preventDefault()
    setFeedback(null)

    const preloadCount = Number(form.preload_count)
    const sampleRate = Number(form.qos_sample_rate)
    const bufferMs = Number(form.buffer_ms)
    if (!Number.isInteger(preloadCount) || preloadCount < 1 || preloadCount > 32) {
      setFeedback({ text: '预加载条数需为 1–32 的整数', tone: 'error' })
      return
    }
    if (Number.isNaN(sampleRate) || sampleRate < 0.01 || sampleRate > 1) {
      setFeedback({ text: 'QoS 采样率需在 0.01–1 之间', tone: 'error' })
      return
    }
    if (!Number.isInteger(bufferMs) || bufferMs < 500 || bufferMs > 10000) {
      setFeedback({ text: '缓冲时长需为 500–10000 的整数毫秒', tone: 'error' })
      return
    }

    setSaving(true)
    try {
      const data = await api.savePlaybackConfig({
        preload_count: preloadCount,
        qos_sample_rate: sampleRate,
        buffer_ms: bufferMs,
      })
      primePlaybackConfig(data)
      setForm({
        preload_count: String(data.preload_count),
        qos_sample_rate: String(data.qos_sample_rate),
        buffer_ms: String(data.buffer_ms),
      })
      setFeedback({ text: '配置已保存', tone: 'success' })
      preload.retry()
    } catch (err) {
      setFeedback({ text: err.message, tone: 'error' })
    } finally {
      setSaving(false)
    }
  }

  const preloadItems = (preload.data && preload.data.items) || []

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
            <span className="pf-field-hint">
              范围 1–32，默认 8；该值决定服务端预加载队列的条数，Feed 首屏后会按此条数预取封面与视频元信息。
            </span>
          </div>
          <div className="pf-field">
            <label htmlFor="pf-buffer-ms">缓冲时长（毫秒）</label>
            <input
              id="pf-buffer-ms"
              type="number"
              inputMode="numeric"
              min="500"
              max="10000"
              step="50"
              value={form.buffer_ms}
              onChange={(event) => updateField('buffer_ms', event.target.value)}
              disabled={saving || config.loading}
              required
            />
            <span className="pf-field-hint">范围 500–10000，默认 2000。</span>
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

        {feedback ? (
          <Toast message={feedback.text} tone={feedback.tone} onDismiss={() => setFeedback(null)} />
        ) : null}

        <div className="pf-form-actions">
          <button type="submit" className="pf-primary-btn" disabled={saving || config.loading}>
            {saving ? <Spinner label="正在保存配置" /> : null}
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
              当前配置命中 <span className="pf-num">{preloadItems.length}</span> 条内容
            </p>
          </div>
          <button type="button" className="pf-ghost-btn" onClick={preload.retry} disabled={preload.loading}>
            {preload.loading ? <Spinner label="正在刷新列表" /> : null}
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

        {!preload.error && !preload.loading && preloadItems.length === 0 ? (
          <EmptyState
            icon="sliders"
            title="队列为空"
            description="调整预加载条数后刷新，或等待新内容发布。"
          />
        ) : null}

        {!preload.error && preloadItems.length > 0 ? (
          <ul className="pf-chip-list" aria-label="预加载内容列表">
            {preloadItems.map((item) => (
              <li key={item.clip_id} className="pf-chip">
                <span className="pf-chip-label">内容</span>
                <span className="pf-num">{item.clip_id}</span>
                {item.duration_ms ? (
                  <span className="pf-chip-label">{Math.round(item.duration_ms / 1000)}s</span>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </section>
    </section>
  )
}
