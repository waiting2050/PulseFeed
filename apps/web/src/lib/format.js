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

// formatDuration 把毫秒时长格式化为 mm:ss；非正数或非法值返回空串，供调用方跳过角标。
export function formatDuration(ms) {
  const total = Math.round((Number(ms) || 0) / 1000)
  if (!Number.isFinite(total) || total <= 0) return ''
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
}
