// Avatar.jsx 渲染圆形头像：有图显示图片，无图用昵称首字占位。
import React from 'react'

export default function Avatar({ name, src, size = 'md' }) {
  const initial = (name || '?').trim().slice(0, 1) || '?'
  const className = size === 'sm' ? 'pf-avatar pf-avatar-sm' : 'pf-avatar'
  if (src) {
    return <img className={className} src={src} alt="" aria-hidden="true" />
  }
  return (
    <span className={className} aria-hidden="true">
      {initial}
    </span>
  )
}
