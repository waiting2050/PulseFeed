// SceneTabs.jsx 提供 Feed 四场景切换，使用 tablist 语义与键盘可达的按钮。
import React from 'react'

export default function SceneTabs({ scenes, scene, onChange, hint }) {
  return (
    <div className="pf-scene-row">
      <div className="pf-scene-tabs" role="tablist" aria-label="内容场景">
        {scenes.map((entry) => (
          <button
            key={entry.key}
            type="button"
            role="tab"
            className="pf-scene"
            aria-selected={scene === entry.key}
            onClick={() => onChange(entry.key)}
          >
            {entry.label}
          </button>
        ))}
      </div>
      {hint ? <span className="pf-scene-hint">{hint}</span> : null}
    </div>
  )
}
