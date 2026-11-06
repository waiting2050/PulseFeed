// TopBar.jsx 提供全局导航外壳：品牌、页面切换、未读数、用户头像与退出。
import React from 'react'
import Icon from './Icon.jsx'
import Avatar from './Avatar.jsx'

const TABS = [
  { key: 'feed', label: '首页' },
  { key: 'publish', label: '发布' },
  { key: 'messages', label: '消息' },
  { key: 'playback', label: '播放' },
]

export default function TopBar({ tab, onTab, unread, me, onLogout }) {
  return (
    <header className="pf-topbar">
      <div className="pf-topbar-inner">
        <div className="pf-brand">
          <span className="pf-brand-mark" aria-hidden="true">
            <Icon name="pulse" size={20} />
          </span>
          <span className="pf-brand-name">
            Pulse<em>Feed</em>
          </span>
        </div>

        <nav className="pf-nav" aria-label="主导航">
          {TABS.map((entry) => (
            <button
              key={entry.key}
              type="button"
              className="pf-nav-item"
              aria-current={tab === entry.key ? 'page' : undefined}
              onClick={() => onTab(entry.key)}
            >
              {entry.label}
              {entry.key === 'messages' && unread > 0 ? (
                <span className="pf-nav-badge" aria-label={`未读 ${unread} 条`}>
                  {unread}
                </span>
              ) : null}
            </button>
          ))}
        </nav>

        <div className="pf-topbar-right">
          <Avatar name={(me && me.nickname) || '我'} />
          <button type="button" className="pf-icon-btn" onClick={onLogout} aria-label="退出登录">
            <Icon name="logout" size={20} />
          </button>
        </div>
      </div>
    </header>
  )
}
