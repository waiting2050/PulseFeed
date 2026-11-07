// SkeletonCard.jsx 渲染首屏/翻页占位卡片，避免加载时出现空白。
import React from 'react'

export default function SkeletonCard() {
  return (
    <div className="pf-skeleton" aria-hidden="true">
      <div className="pf-sk-cover" />
      <div className="pf-sk-line" />
      <div className="pf-sk-line short" />
      <div className="pf-sk-actions" />
    </div>
  )
}
