// EmptyState.jsx 统一空态：图标、标题、说明与可选主按钮。
import React from 'react'
import Icon from './Icon.jsx'

export default function EmptyState({ title, description, actionLabel, onAction, icon = 'spark' }) {
  return (
    <div className="pf-empty" role="status">
      <span className="pf-empty-icon">
        <Icon name={icon} size={24} />
      </span>
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
      {actionLabel && onAction ? (
        <button type="button" className="pf-primary-btn" onClick={onAction}>
          {actionLabel}
        </button>
      ) : null}
    </div>
  )
}
