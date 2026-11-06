// useApi.js 统一页面读取请求的加载、错误与重试状态，并丢弃过期响应。
import { useCallback, useEffect, useRef, useState } from 'react'

export default function useApi(loader, deps = [], { immediate = true } = {}) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(immediate)
  const [error, setError] = useState('')
  const seqRef = useRef(0)
  const loaderRef = useRef(loader)
  loaderRef.current = loader

  const run = useCallback(async () => {
    const seq = ++seqRef.current
    setLoading(true)
    setError('')
    try {
      const result = await loaderRef.current()
      if (seq === seqRef.current) {
        setData(result)
        return result
      }
      return undefined
    } catch (err) {
      if (seq === seqRef.current) {
        setError(err && err.message ? err.message : '请求失败')
      }
      return undefined
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (immediate) run()
    return () => {
      seqRef.current += 1
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, immediate])

  return { data, loading, error, retry: run, setData, setError }
}
