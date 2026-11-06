// Spinner.jsx 按钮内联加载指示：只占位不改变按钮尺寸，带无障碍状态标签。
import React from 'react'

export default function Spinner({ label = '处理中' }) {
  return <span className="pf-spinner" role="status" aria-label={label} />
}
