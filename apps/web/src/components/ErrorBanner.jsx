// ErrorBanner.jsx 渲染内联错误提示，保留已有内容并提供重试入口。
import React from 'react'

export default function ErrorBanner({ message, onRetry }) {
  if (!message) return null
  return (
    <div className="pf-error" role="alert">
      <span className="pf-error-text">{message}</span>
      {onRetry ? (
        <button type="button" className="pf-error-retry" onClick={onRetry}>
          重试
        </button>
      ) : null}
    </div>
  )
}
