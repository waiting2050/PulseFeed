// Toast.jsx 统一轻提示：自动消失、可携带一个操作按钮，错误提示停留更久；离开页面即清理定时器。
import React, { useEffect, useRef } from 'react'

const TIMEOUT = {
  error: 4200,
  success: 2800,
  info: 2800,
}

export default function Toast({ message, tone = 'info', action, onDismiss }) {
  const dismissRef = useRef(onDismiss)
  dismissRef.current = onDismiss

  useEffect(() => {
    if (!message) return undefined
    const timer = window.setTimeout(() => {
      if (dismissRef.current) dismissRef.current()
    }, TIMEOUT[tone] || TIMEOUT.info)
    return () => window.clearTimeout(timer)
  }, [message, tone])

  if (!message) return null

  return (
    <div className={`pf-toast pf-toast-${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      <span className="pf-toast-text">{message}</span>
      {action ? (
        <button type="button" className="pf-toast-action" onClick={action.onClick}>
          {action.label}
        </button>
      ) : null}
      <button
        type="button"
        className="pf-toast-close"
        onClick={() => onDismiss && onDismiss()}
        aria-label="关闭提示"
      >
        关闭
      </button>
    </div>
  )
}
