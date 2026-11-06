// ClipCover.jsx 统一 16:9 封面：优先使用封面图，缺失或加载失败时回退为低饱和暗色块 + 标题首字。
import React, { useState } from 'react'

const GRADIENTS = [
  'linear-gradient(140deg, #1c1d22 0%, #2e2a2d 100%)',
  'linear-gradient(140deg, #171f22 0%, #1f3034 100%)',
  'linear-gradient(140deg, #211c16 0%, #33281c 100%)',
  'linear-gradient(140deg, #161f19 0%, #233028 100%)',
  'linear-gradient(140deg, #1c1822 0%, #2a2336 100%)',
  'linear-gradient(140deg, #221a1c 0%, #33272a 100%)',
]

function hashOf(value) {
  const text = String(value == null ? '' : value)
  let hash = 0
  for (let i = 0; i < text.length; i += 1) {
    hash = (hash * 31 + text.charCodeAt(i)) % 997
  }
  return hash
}

export default function ClipCover({ clipId, title, coverUrl, className = '', children }) {
  const [broken, setBroken] = useState(false)
  const gradient = GRADIENTS[hashOf(clipId) % GRADIENTS.length]

  return (
    <div className={`pf-cover ${className}`.trim()}>
      {coverUrl && !broken ? (
        <img
          src={coverUrl}
          alt={`${title} 封面`}
          loading="lazy"
          decoding="async"
          onError={() => setBroken(true)}
        />
      ) : (
        <span className="pf-cover-fallback" style={{ background: gradient }}>
          <span className="pf-cover-initial" aria-hidden="true">
            {title.slice(0, 1)}
          </span>
        </span>
      )}
      {children}
    </div>
  )
}
