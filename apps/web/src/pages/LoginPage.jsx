// LoginPage.jsx 提供登录/注册视图：左侧品牌舞台 + 右侧表单卡，窄屏收为单列。
import React from 'react'
import Icon from '../components/Icon.jsx'
import Spinner from '../components/Spinner.jsx'

export default function LoginPage({
  mode,
  onMode,
  account,
  onAccount,
  password,
  onPassword,
  nickname,
  onNickname,
  authError,
  busy,
  onSubmit,
}) {
  return (
    <main className="pf-login">
      <div className="pf-login-wrap">
        <section className="pf-stage" aria-hidden="true">
          <div className="pf-brand pf-stage-brand">
            <span className="pf-brand-mark">
              <Icon name="pulse" size={20} />
            </span>
            <span className="pf-brand-name">
              Pulse<em>Feed</em>
            </span>
          </div>
          <h1>把此刻的灵感，变成下一帧。</h1>
          <p className="pf-stage-lead">一个轻量的内容流产品：浏览、发布、互动与播放偏好，都收在同一处。</p>
          <ul className="pf-stage-list">
            <li>
              <span className="pf-dot" />
              <span>
                <b>四个场景</b>，随时切换最新、热门、关注与推荐。
              </span>
            </li>
            <li>
              <span className="pf-dot" />
              <span>
                <b>沉浸浏览</b>，16:9 封面与暗色界面让内容本身成为焦点。
              </span>
            </li>
            <li>
              <span className="pf-dot" />
              <span>
                <b>即时互动</b>，点赞、收藏与评论都无需等待页面刷新。
              </span>
            </li>
          </ul>
        </section>

        <section className="pf-login-card" aria-labelledby="pf-login-title">
          <h2 id="pf-login-title">{mode === 'login' ? '欢迎回来' : '创建账号'}</h2>
          <p className="pf-login-sub">
            {mode === 'login' ? '登录后即可浏览内容流并参与互动。' : '注册完成后会自动登录。'}
          </p>

          <div className="pf-seg" role="tablist" aria-label="登录或注册">
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'login'}
              onClick={() => onMode('login')}
            >
              登录
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'register'}
              onClick={() => onMode('register')}
            >
              注册
            </button>
          </div>

          <form onSubmit={onSubmit} noValidate>
            <div className="pf-field">
              <label htmlFor="pf-account">账号</label>
              <input
                id="pf-account"
                value={account}
                onChange={(event) => onAccount(event.target.value)}
                autoComplete="username"
                placeholder="请输入账号"
                required
              />
            </div>
            <div className="pf-field">
              <label htmlFor="pf-password">密码</label>
              <input
                id="pf-password"
                type="password"
                value={password}
                onChange={(event) => onPassword(event.target.value)}
                autoComplete={mode === 'register' ? 'new-password' : 'current-password'}
                placeholder="请输入密码"
                required
              />
            </div>
            {mode === 'register' ? (
              <div className="pf-field">
                <label htmlFor="pf-nickname">昵称（可选）</label>
                <input
                  id="pf-nickname"
                  value={nickname}
                  onChange={(event) => onNickname(event.target.value)}
                  autoComplete="nickname"
                  maxLength={64}
                  placeholder="不填则使用默认昵称"
                />
              </div>
            ) : null}

            {authError ? (
              <p className="pf-form-error" role="alert" aria-live="polite">
                {authError}
              </p>
            ) : null}

            <button type="submit" className="pf-primary-btn pf-login-submit" disabled={busy}>
              {busy ? <Spinner label="正在登录" /> : null}
              {busy ? '处理中…' : mode === 'login' ? '登录' : '注册并登录'}
            </button>
          </form>

          <div className="pf-login-foot">
            <span>{mode === 'login' ? '还没有账号？' : '已有账号？'}</span>
            <button
              type="button"
              className="pf-ghost-btn"
              onClick={() => onMode(mode === 'login' ? 'register' : 'login')}
            >
              {mode === 'login' ? '去注册' : '去登录'}
            </button>
          </div>
        </section>
      </div>
    </main>
  )
}
