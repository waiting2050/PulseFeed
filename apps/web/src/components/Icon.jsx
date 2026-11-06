// Icon.jsx 提供 P0 所需的少量内联 SVG 图标，避免引入图标库。
import React from 'react'

const GLYPHS = {
  pulse: { paths: ['M2 12h4l3-8 4 16 3-8h6'], solidOnly: false },
  search: { paths: ['M18 11a7 7 0 1 1-14 0 7 7 0 0 1 14 0', 'M21 21l-4.3-4.3'] },
  bell: { paths: ['M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9', 'M13.7 21a2 2 0 0 1-3.4 0'] },
  heart: { paths: ['M12 21s-7-4.6-9.3-8.1A5 5 0 0 1 12 6a5 5 0 0 1 9.3 6.9C18.3 16.4 12 21 12 21z'] },
  bookmark: { paths: ['M6 4h12v17l-6-4-6 4z'] },
  comment: { paths: ['M21 12a8 8 0 0 1-8 8H7l-4 3V12a8 8 0 0 1 8-8h2a8 8 0 0 1 8 8z'] },
  play: { paths: ['M8 5v14l11-7z'], solidOnly: true },
  upload: { paths: ['M12 16V4', 'M7 9l5-5 5 5', 'M4 20h16'] },
  mail: { paths: ['M3 6h18v12H3z', 'M3 7l9 6 9-6'] },
  sliders: { paths: ['M4 7h10', 'M18 7h2', 'M4 17h4', 'M12 17h8', 'M14 4v6', 'M8 14v6'] },
  logout: { paths: ['M15 4h4a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-4', 'M10 17l-5-5 5-5', 'M5 12h11'] },
  refresh: { paths: ['M20 11a8 8 0 1 0-2.3 6.3', 'M20 4v7h-7'] },
  spark: { paths: ['M12 3l2.2 6.8L21 12l-6.8 2.2L12 21l-2.2-6.8L3 12l6.8-2.2z'] },
  user: { paths: ['M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M4 21a8 8 0 0 1 16 0'] },
}

// Icon 统一按 24 网格绘制，size 取 20 / 24，线宽统一 1.5，filled 用于已激活的实心状态。
export default function Icon({ name, size = 20, filled = false, strokeWidth = 1.5, ...rest }) {
  const glyph = GLYPHS[name]
  if (!glyph) return null
  const solid = filled || glyph.solidOnly
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      aria-hidden="true"
      focusable="false"
      fill={solid ? 'currentColor' : 'none'}
      stroke={solid ? 'none' : 'currentColor'}
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...rest}
    >
      {glyph.paths.map((d) => (
        <path key={d} d={d} />
      ))}
    </svg>
  )
}
