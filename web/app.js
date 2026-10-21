const API_BASE = '/api';
const PAGE_SIZE = 10;
const COMMENT_PAGE_SIZE = 10;
const NOTIFICATION_PAGE_SIZE = 12;

const state = {
  token: localStorage.getItem('pulsefeed_token') || '',
  user: null,
  scene: 'recommend',
  unread: 0,
};

const feed = {
  items: [],
  cursor: '',
  hasMore: true,
  loading: false,
  token: 0,
};

const commentStates = new Map();
const notifications = { items: [], cursor: '', hasMore: true, loading: false };
const authMode = { value: 'login' };
let feedStateAction = null;

const $ = (id) => document.getElementById(id);

async function request(path, options = {}) {
  const { method = 'GET', body, auth = true, headers = {} } = options;
  const finalHeaders = { ...headers };
  if (auth && state.token) {
    finalHeaders.Authorization = `Bearer ${state.token}`;
  }
  const isForm = body instanceof FormData;
  if (body && !isForm) {
    finalHeaders['Content-Type'] = 'application/json';
  }

  let response;
  try {
    response = await fetch(API_BASE + path, {
      method,
      headers: finalHeaders,
      body: isForm ? body : body ? JSON.stringify(body) : undefined,
    });
  } catch (error) {
    throw new Error('网络异常，请检查服务是否已启动');
  }

  if (response.status === 401 && auth) {
    clearSession();
    renderSession();
    throw new Error('登录状态已失效，请重新登录');
  }
  if (response.status === 204) {
    return null;
  }

  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.code !== 0) {
    throw new Error(payload.message || `请求失败（${response.status}）`);
  }
  return payload.data;
}

function toast(message, kind = 'info') {
  const node = document.createElement('div');
  node.className = `toast ${kind === 'error' ? 'error' : ''}`.trim();
  node.textContent = message;
  $('toasts').appendChild(node);
  setTimeout(() => node.remove(), 3200);
}

function formatCount(value) {
  const num = Number(value) || 0;
  if (num >= 10000) {
    return `${(num / 10000).toFixed(1)}w`;
  }
  return String(num);
}

function formatTime(value) {
  if (!value) {
    return '';
  }
  const time = new Date(value);
  if (Number.isNaN(time.getTime())) {
    return '';
  }
  const diff = Date.now() - time.getTime();
  const minute = 60 * 1000;
  if (diff < minute) {
    return '刚刚';
  }
  if (diff < 60 * minute) {
    return `${Math.floor(diff / minute)} 分钟前`;
  }
  if (diff < 24 * 60 * minute) {
    return `${Math.floor(diff / (60 * minute))} 小时前`;
  }
  if (diff < 7 * 24 * 60 * minute) {
    return `${Math.floor(diff / (24 * 60 * minute))} 天前`;
  }
  return time.toLocaleDateString();
}

function clearSession() {
  state.token = '';
  state.user = null;
  state.unread = 0;
  localStorage.removeItem('pulsefeed_token');
}

function saveSession(token, user) {
  state.token = token;
  state.user = user;
  localStorage.setItem('pulsefeed_token', token);
}

function requireLogin() {
  if (state.user) {
    return true;
  }
  openAuth('login');
  return false;
}

function openAuth(mode) {
  switchAuthMode(mode || 'login');
  $('auth-hint').textContent = '';
  $('modal-auth').hidden = false;
  $('auth-account').focus();
}

function switchAuthMode(mode) {
  authMode.value = mode;
  const register = mode === 'register';
  $('auth-title').textContent = register ? '注册' : '登录';
  $('auth-submit').textContent = register ? '注册并登录' : '登录';
  $('nickname-field').hidden = !register;
  $('tab-login').classList.toggle('active', !register);
  $('tab-register').classList.toggle('active', register);
  $('auth-password').autocomplete = register ? 'new-password' : 'current-password';
}

function closeModal(id) {
  $(id).hidden = true;
}

function renderSession() {
  const loggedIn = Boolean(state.user);
  $('btn-notifications').hidden = !loggedIn;
  $('btn-publish').hidden = !loggedIn;
  updateUnreadBadge();

  const area = $('session-area');
  area.textContent = '';
  if (!loggedIn) {
    const login = document.createElement('button');
    login.className = 'ghost-btn';
    login.textContent = '登录';
    login.addEventListener('click', () => openAuth('login'));
    const register = document.createElement('button');
    register.className = 'primary-btn';
    register.textContent = '注册';
    register.addEventListener('click', () => openAuth('register'));
    area.append(login, register);
    return;
  }

  const user = state.user;
  if (user.avatar_url) {
    const avatar = document.createElement('img');
    avatar.className = 'avatar';
    avatar.src = user.avatar_url;
    avatar.alt = user.nickname || user.account;
    area.appendChild(avatar);
  } else {
    const fallback = document.createElement('span');
    fallback.className = 'avatar-fallback';
    fallback.textContent = (user.nickname || user.account || 'U').slice(0, 1);
    area.appendChild(fallback);
  }

  const name = document.createElement('span');
  name.className = 'user-name';
  name.textContent = user.nickname || user.account;
  area.appendChild(name);

  const logout = document.createElement('button');
  logout.className = 'ghost-btn';
  logout.textContent = '退出';
  logout.addEventListener('click', logoutCurrent);
  area.appendChild(logout);
}

async function logoutCurrent() {
  try {
    await request('/sessions/current', { method: 'DELETE' });
  } catch (error) {
    // 退出接口失败也清理本地登录状态
  }
  clearSession();
  renderSession();
  closeModal('modal-notifications');
  toast('已退出登录');
  loadFeed(true);
}

function sceneLabel(scene) {
  return { recommend: '推荐', hot: '热门', timeline: '最新', following: '关注' }[scene] || '推荐';
}

function sceneNeedsLogin(scene) {
  return scene === 'recommend' || scene === 'following';
}

function scenePath(scene, cursorValue) {
  const cursorParam = cursorValue ? `&cursor=${encodeURIComponent(cursorValue)}` : '';
  if (scene === 'recommend') {
    return `/feed-items/recommend?limit=${PAGE_SIZE}${cursorParam}`;
  }
  if (scene === 'following') {
    return `/feed-items/following?limit=${PAGE_SIZE}${cursorParam}`;
  }
  return `/feed-items?scene=${scene}&limit=${PAGE_SIZE}${cursorParam}`;
}

async function loadFeed(reset = false) {
  if (reset) {
    feed.token += 1;
    feed.items = [];
    feed.cursor = '';
    feed.hasMore = true;
    feed.loading = false;
    $('feed-list').textContent = '';
    setFeedState('');
  }

  if (feed.loading || !feed.hasMore) {
    return;
  }
  if (sceneNeedsLogin(state.scene) && !state.user) {
    setFeedState(`登录后查看${sceneLabel(state.scene)}内容`, '去登录', () => openAuth('login'));
    return;
  }

  const token = feed.token;
  feed.loading = true;
  if (feed.items.length === 0) {
    setFeedState('加载中…');
  }

  try {
    const data = await request(scenePath(state.scene, feed.cursor));
    if (token !== feed.token) {
      return;
    }
    feed.items.push(...(data.items || []));
    feed.cursor = data.next_cursor || '';
    feed.hasMore = Boolean(data.has_more);
    renderFeedItems(data.items || []);
    if (feed.items.length === 0) {
      setFeedState('这里还没有内容');
    } else if (!feed.hasMore) {
      setFeedState('已经到底了');
    } else {
      setFeedState('');
    }
  } catch (error) {
    setFeedState(error.message);
    toast(error.message, 'error');
  } finally {
    if (token === feed.token) {
      feed.loading = false;
    }
  }
}

function setFeedState(message, actionLabel, action) {
  const holder = $('feed-state');
  holder.textContent = '';
  feedStateAction = action || null;
  if (!message) {
    return;
  }
  const text = document.createElement('div');
  text.textContent = message;
  holder.appendChild(text);
  if (actionLabel && action) {
    const button = document.createElement('button');
    button.className = 'primary-btn';
    button.textContent = actionLabel;
    button.addEventListener('click', () => {
      if (feedStateAction) {
        feedStateAction();
      }
    });
    holder.appendChild(button);
  }
}

function renderFeedItems(items) {
  const list = $('feed-list');
  items.forEach((item) => list.appendChild(createCard(item)));
}

function createCard(item) {
  const card = document.createElement('article');
  card.className = 'card';
  card.dataset.videoId = String(item.video_id);

  const media = document.createElement('div');
  media.className = 'card-media';
  if (item.media_url) {
    const video = document.createElement('video');
    video.controls = true;
    video.preload = 'metadata';
    video.src = item.media_url;
    if (item.cover_url) {
      video.poster = item.cover_url;
    }
    media.appendChild(video);
  } else {
    const placeholder = document.createElement('div');
    placeholder.className = 'media-placeholder';
    placeholder.textContent = '暂无视频文件';
    media.appendChild(placeholder);
  }

  const body = document.createElement('div');
  body.className = 'card-body';

  const title = document.createElement('h3');
  title.className = 'card-title';
  title.textContent = item.title || '无标题';
  body.appendChild(title);

  const meta = document.createElement('div');
  meta.className = 'card-meta';
  const author = document.createElement('span');
  author.textContent = `作者 #${item.author_id}`;
  const time = document.createElement('span');
  time.textContent = formatTime(item.published_at);
  meta.append(author, time);
  body.appendChild(meta);

  const actions = document.createElement('div');
  actions.className = 'card-actions';

  const likeBtn = createActionButton('赞', item.like_count);
  likeBtn.addEventListener('click', () => toggleAction(card, likeBtn, 'likes'));
  const favoriteBtn = createActionButton('收藏', item.favorite_count);
  favoriteBtn.addEventListener('click', () => toggleAction(card, favoriteBtn, 'favorites'));
  const commentBtn = createActionButton('评论', item.comment_count);
  const comments = createComments(item.video_id);
  commentBtn.addEventListener('click', () => toggleComments(card, comments, item.video_id));

  actions.append(likeBtn, favoriteBtn, commentBtn);

  if (state.user && state.user.id !== item.author_id) {
    const followBtn = createFollowButton(item.author_id);
    actions.appendChild(followBtn);
  }

  body.appendChild(actions);
  card.append(media, body, comments);
  return card;
}

function createActionButton(label, count) {
  const button = document.createElement('button');
  button.className = 'action-btn';
  const text = document.createElement('span');
  text.className = 'label';
  text.textContent = label;
  const num = document.createElement('span');
  num.className = 'count';
  num.textContent = formatCount(count);
  button.dataset.count = String(Number(count) || 0);
  button.append(text, num);
  return button;
}

function createFollowButton(authorID) {
  const button = document.createElement('button');
  button.className = 'action-btn';
  const label = document.createElement('span');
  label.className = 'label';
  label.textContent = '关注';
  button.appendChild(label);
  button.dataset.authorId = String(authorID);
  button.addEventListener('click', () => toggleFollow(button));
  return button;
}

async function toggleFollow(button) {
  if (!requireLogin()) {
    return;
  }
  const authorID = button.dataset.authorId;
  const following = button.classList.contains('following');
  button.disabled = true;
  try {
    if (following) {
      await request(`/users/${authorID}/follow`, { method: 'DELETE' });
      setFollowState(button, false);
    } else {
      const data = await request(`/users/${authorID}/follow`, { method: 'POST' });
      setFollowState(button, Boolean(data && data.following));
    }
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    button.disabled = false;
  }
}

function setFollowState(button, following) {
  button.classList.toggle('following', following);
  button.querySelector('.label').textContent = following ? '已关注' : '关注';
}

async function toggleAction(card, button, path) {
  if (!requireLogin()) {
    return;
  }
  const active = button.classList.contains('active');
  button.disabled = true;
  try {
    const data = await request(`/videos/${card.dataset.videoId}/${path}`, {
      method: active ? 'DELETE' : 'POST',
    });
    const nowActive = Boolean(data && data.active);
    const current = Number(button.dataset.count) || 0;
    let next = current;
    if (nowActive && !active) {
      next = current + 1;
    } else if (!nowActive && active) {
      next = Math.max(0, current - 1);
    }
    button.dataset.count = String(next);
    button.querySelector('.count').textContent = formatCount(next);
    button.classList.toggle('active', nowActive);
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    button.disabled = false;
  }
}

function createComments(videoID) {
  const section = document.createElement('section');
  section.className = 'comments';
  section.hidden = true;

  const form = document.createElement('form');
  form.className = 'comment-form';
  const input = document.createElement('input');
  input.placeholder = '写下你的评论…';
  input.maxLength = 500;
  const submit = document.createElement('button');
  submit.className = 'primary-btn';
  submit.type = 'submit';
  submit.textContent = '发送';
  form.append(input, submit);
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    submitComment(videoID, input, submit);
  });

  const list = document.createElement('ul');
  list.className = 'comment-list';

  const more = document.createElement('button');
  more.className = 'text-btn more-btn';
  more.textContent = '加载更多评论';
  more.hidden = true;
  more.addEventListener('click', () => loadComments(videoID, section, true));

  section.append(form, list, more);
  return section;
}

function toggleComments(card, section, videoID) {
  if (!section.hidden) {
    section.hidden = true;
    return;
  }
  section.hidden = false;
  const current = commentStates.get(videoID);
  if (current && current.loaded) {
    return;
  }
  loadComments(videoID, section, false);
}

async function loadComments(videoID, section, more) {
  const current = commentStates.get(videoID) || {
    items: [],
    cursor: '',
    hasMore: true,
    loading: false,
    loaded: false,
  };
  if (current.loading || (more && !current.hasMore)) {
    return;
  }

  current.loading = true;
  const cursorParam = more && current.cursor ? `&cursor=${encodeURIComponent(current.cursor)}` : '';
  try {
    const data = await request(
      `/videos/${videoID}/comments?limit=${COMMENT_PAGE_SIZE}${cursorParam}`,
      { auth: false },
    );
    current.items.push(...(data.items || []));
    current.cursor = data.next_cursor || '';
    current.hasMore = Boolean(data.has_more);
    current.loaded = true;
    commentStates.set(videoID, current);
    renderComments(videoID, section, current);
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    current.loading = false;
  }
}

function renderComments(videoID, section, current) {
  const list = section.querySelector('.comment-list');
  list.textContent = '';
  current.items.forEach((comment) => {
    const item = document.createElement('li');
    item.className = 'comment-item';
    const content = document.createElement('p');
    content.textContent = comment.content;
    const meta = document.createElement('div');
    meta.className = 'comment-meta';
    const info = document.createElement('span');
    info.textContent = `用户 #${comment.user_id} · ${formatTime(comment.created_at)}`;
    meta.appendChild(info);
    if (state.user && state.user.id === comment.user_id) {
      const remove = document.createElement('button');
      remove.className = 'text-btn';
      remove.textContent = '删除';
      remove.addEventListener('click', () => deleteComment(videoID, comment.id, item));
      meta.appendChild(remove);
    }
    item.append(content, meta);
    list.appendChild(item);
  });
  section.querySelector('.more-btn').hidden = !current.hasMore;
}

async function submitComment(videoID, input, submit) {
  if (!requireLogin()) {
    return;
  }
  const content = input.value.trim();
  if (!content) {
    toast('评论内容不能为空', 'error');
    return;
  }
  submit.disabled = true;
  try {
    const comment = await request(`/videos/${videoID}/comments`, {
      method: 'POST',
      body: { content },
    });
    input.value = '';
    const current = commentStates.get(videoID) || {
      items: [],
      cursor: '',
      hasMore: false,
      loading: false,
      loaded: true,
    };
    current.items.unshift(comment);
    current.loaded = true;
    commentStates.set(videoID, current);
    const section = input.closest('.comments');
    renderComments(videoID, section, current);
    bumpCardCount(videoID, '评论', 1);
    toast('评论已发布');
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    submit.disabled = false;
  }
}

async function deleteComment(videoID, commentID, node) {
  const section = node.closest('.comments');
  try {
    await request(`/comments/${commentID}`, { method: 'DELETE' });
    const current = commentStates.get(videoID);
    if (current) {
      current.items = current.items.filter((item) => item.id !== commentID);
      commentStates.set(videoID, current);
      if (section) {
        renderComments(videoID, section, current);
      }
    }
    bumpCardCount(videoID, '评论', -1);
    toast('评论已删除');
  } catch (error) {
    toast(error.message, 'error');
  }
}

function bumpCardCount(videoID, label, delta) {
  const card = document.querySelector(`.card[data-video-id="${videoID}"]`);
  if (!card) {
    return;
  }
  const buttons = card.querySelectorAll('.action-btn');
  for (const button of buttons) {
    const text = button.querySelector('.label');
    if (text && text.textContent === label) {
      const next = Math.max(0, (Number(button.dataset.count) || 0) + delta);
      button.dataset.count = String(next);
      button.querySelector('.count').textContent = formatCount(next);
      return;
    }
  }
}

function updateUnreadBadge() {
  const badge = $('unread-badge');
  if (state.unread > 0) {
    badge.hidden = false;
    badge.textContent = state.unread > 99 ? '99+' : String(state.unread);
  } else {
    badge.hidden = true;
  }
}

async function refreshUnread() {
  if (!state.user) {
    return;
  }
  try {
    const data = await request('/notifications/unread-count');
    state.unread = Number(data.count) || 0;
    updateUnreadBadge();
  } catch (error) {
    // 未读数失败不打扰用户
  }
}

async function openNotifications() {
  if (!requireLogin()) {
    return;
  }
  $('modal-notifications').hidden = false;
  if (notifications.items.length === 0) {
    await loadNotifications(true);
  }
}

async function loadNotifications(reset) {
  if (notifications.loading) {
    return;
  }
  if (reset) {
    notifications.items = [];
    notifications.cursor = '';
    notifications.hasMore = true;
    $('notification-list').textContent = '';
  }
  if (!notifications.hasMore) {
    return;
  }

  notifications.loading = true;
  const cursorParam = notifications.cursor
    ? `&cursor=${encodeURIComponent(notifications.cursor)}`
    : '';
  try {
    const data = await request(
      `/notifications?limit=${NOTIFICATION_PAGE_SIZE}${cursorParam}`,
    );
    notifications.items.push(...(data.items || []));
    notifications.cursor = data.next_cursor || '';
    notifications.hasMore = Boolean(data.has_more);
    renderNotifications();
    $('notification-more').hidden = !notifications.hasMore;
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    notifications.loading = false;
  }
}

function notificationText(item) {
  const actor = item.actor_nickname || `用户 #${item.actor_id}`;
  switch (item.type) {
    case 'like':
      return `${actor} 赞了你的视频`;
    case 'favorite':
      return `${actor} 收藏了你的视频`;
    case 'comment':
      return `${actor} 评论了你的视频`;
    case 'follow':
      return `${actor} 关注了你`;
    default:
      return `${actor} 有新动态`;
  }
}

function renderNotifications() {
  const list = $('notification-list');
  list.textContent = '';
  if (notifications.items.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'empty';
    empty.textContent = '暂无通知';
    list.appendChild(empty);
    return;
  }
  notifications.items.forEach((item) => {
    const card = document.createElement('div');
    card.className = `notification-item ${item.is_read ? '' : 'unread'}`.trim();
    const text = document.createElement('p');
    text.textContent = notificationText(item);
    const meta = document.createElement('span');
    meta.textContent = formatTime(item.created_at);
    card.append(text, meta);
    if (item.content) {
      const excerpt = document.createElement('p');
      excerpt.textContent = `“${item.content}”`;
      card.insertBefore(excerpt, meta);
    }
    list.appendChild(card);
  });
}

async function markAllRead() {
  try {
    await request('/notifications/read', { method: 'POST' });
    state.unread = 0;
    updateUnreadBadge();
    notifications.items.forEach((item) => {
      item.is_read = true;
    });
    renderNotifications();
    toast('已全部标记为已读');
  } catch (error) {
    toast(error.message, 'error');
  }
}

function idempotencyKey() {
  if (window.crypto && typeof window.crypto.randomUUID === 'function') {
    return window.crypto.randomUUID();
  }
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function submitPublish(event) {
  event.preventDefault();
  if (!requireLogin()) {
    return;
  }
  const fileInput = $('publish-file');
  const coverInput = $('publish-cover');
  const titleInput = $('publish-title');
  const submit = $('publish-submit');
  const progress = $('publish-progress');

  const file = fileInput.files[0];
  const title = titleInput.value.trim();
  if (!file || !title) {
    toast('请选择视频并填写标题', 'error');
    return;
  }

  submit.disabled = true;
  progress.textContent = '正在上传视频…';
  try {
    const videoForm = new FormData();
    videoForm.append('kind', 'video');
    videoForm.append('file', file);
    const video = await request('/uploads', { method: 'POST', body: videoForm });

    let coverURL = '';
    const cover = coverInput.files[0];
    if (cover) {
      progress.textContent = '正在上传封面…';
      const coverForm = new FormData();
      coverForm.append('kind', 'cover');
      coverForm.append('file', cover);
      const coverData = await request('/uploads', { method: 'POST', body: coverForm });
      coverURL = coverData.url;
    }

    progress.textContent = '正在发布…';
    await request('/videos', {
      method: 'POST',
      body: { title, media_url: video.url, cover_url: coverURL },
      headers: { 'Idempotency-Key': idempotencyKey() },
    });

    progress.textContent = '';
    fileInput.value = '';
    coverInput.value = '';
    titleInput.value = '';
    closeModal('modal-publish');
    toast('发布成功');
    selectScene('timeline');
  } catch (error) {
    progress.textContent = '';
    toast(error.message, 'error');
  } finally {
    submit.disabled = false;
  }
}

async function submitAuth(event) {
  event.preventDefault();
  const account = $('auth-account').value.trim();
  const password = $('auth-password').value;
  const nickname = $('auth-nickname').value.trim();
  const hint = $('auth-hint');
  const submit = $('auth-submit');

  if (!account || !password) {
    hint.textContent = '账号和密码不能为空';
    return;
  }

  submit.disabled = true;
  hint.textContent = '';
  try {
    if (authMode.value === 'register') {
      await request('/users', {
        method: 'POST',
        auth: false,
        body: { account, password, nickname },
      });
    }
    const data = await request('/sessions', {
      method: 'POST',
      auth: false,
      body: { account, password },
    });
    saveSession(data.access_token, data.user);
    renderSession();
    closeModal('modal-auth');
    $('auth-form').reset();
    toast(`欢迎，${data.user.nickname || data.user.account}`);
    refreshUnread();
    loadFeed(true);
  } catch (error) {
    hint.textContent = error.message;
  } finally {
    submit.disabled = false;
  }
}

function selectScene(scene) {
  if (state.scene === scene && feed.loading) {
    return;
  }
  state.scene = scene;
  document.querySelectorAll('.scene').forEach((button) => {
    button.classList.toggle('active', button.dataset.scene === scene);
  });
  loadFeed(true);
}

function bindEvents() {
  document.querySelectorAll('.scene').forEach((button) => {
    button.addEventListener('click', () => selectScene(button.dataset.scene));
  });

  $('btn-publish').addEventListener('click', () => {
    if (requireLogin()) {
      $('modal-publish').hidden = false;
    }
  });
  $('btn-notifications').addEventListener('click', openNotifications);
  $('mark-read').addEventListener('click', markAllRead);
  $('notification-more').addEventListener('click', () => loadNotifications(false));

  $('auth-form').addEventListener('submit', submitAuth);
  $('publish-form').addEventListener('submit', submitPublish);

  $('tab-login').addEventListener('click', () => switchAuthMode('login'));
  $('tab-register').addEventListener('click', () => switchAuthMode('register'));

  document.querySelectorAll('[data-close]').forEach((button) => {
    button.addEventListener('click', () => closeModal(button.dataset.close));
  });
  document.querySelectorAll('.modal').forEach((modal) => {
    modal.addEventListener('click', (event) => {
      if (event.target === modal) {
        modal.hidden = true;
      }
    });
  });

  window.addEventListener('scroll', () => {
    if (feed.items.length === 0 || feed.loading || !feed.hasMore) {
      return;
    }
    const nearBottom =
      window.innerHeight + window.scrollY >= document.body.offsetHeight - 240;
    if (nearBottom) {
      loadFeed(false);
    }
  });

  window.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      document.querySelectorAll('.modal').forEach((modal) => {
        modal.hidden = true;
      });
    }
  });
}

async function restoreSession() {
  try {
    state.user = await request('/users/me');
  } catch (error) {
    clearSession();
  }
}

async function init() {
  bindEvents();
  renderSession();
  if (state.token) {
    await restoreSession();
    renderSession();
    refreshUnread();
    setInterval(refreshUnread, 60000);
  }
  loadFeed(true);
}

init();
