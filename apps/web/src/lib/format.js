// format.js 提供计数、时间与首字的统一格式化，保证数字使用等宽字形、时间口径一致。
export function formatCount(value) {
  const count = Number(value) || 0
  if (count >= 10000) {
    const wan = count / 10000
    return `${wan >= 10 ? Math.round(wan) : wan.toFixed(1).replace(/\.0$/, '')}w`
  }
  if (count >= 1000) {
    const kilo = count / 1000
    return `${kilo.toFixed(1).replace(/\.0$/, '')}k`
  }
  return String(count)
}

export function formatTime(value) {
  if (!value) return ''
  const then = new Date(value).getTime()
  if (Number.isNaN(then)) return ''
  const diff = Date.now() - then
  if (diff < 60 * 1000) return '刚刚'
  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / 60000)} 分钟前`
  if (diff < 24 * 60 * 60 * 1000) return `${Math.floor(diff / 3600000)} 小时前`
  if (diff < 7 * 24 * 60 * 60 * 1000) return `${Math.floor(diff / 86400000)} 天前`
  return new Date(then).toLocaleDateString()
}

export function formatDateTime(value) {
  if (!value) return ''
  const then = new Date(value)
  if (Number.isNaN(then.getTime())) return ''
  return then.toLocaleString()
}

export function initialOf(value, fallback = '?') {
  const text = String(value == null ? '' : value).trim()
  return text ? text.slice(0, 1) : fallback
}

export function formatFileSize(bytes) {
  const size = Number(bytes) || 0
  if (size >= 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)} MB`
  if (size >= 1024) return `${Math.round(size / 1024)} KB`
  return `${size} B`
}
