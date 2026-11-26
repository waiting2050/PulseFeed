import assert from 'node:assert/strict'
import { beforeEach, test } from 'node:test'
import {
  clearAllFeedProgress,
  clearFeedProgress,
  feedCacheOwner,
  loadFeedProgress,
  saveFeedProgress,
} from './feedProgress.js'

class MemoryStorage {
  data = new Map()
  get length() { return this.data.size }
  key(index) { return [...this.data.keys()][index] ?? null }
  getItem(key) { return this.data.get(key) ?? null }
  setItem(key, value) { this.data.set(key, String(value)) }
  removeItem(key) { this.data.delete(key) }
}

const progress = { items: [{ clip_id: 7, liked: true }], cursor: 'next', hasMore: true }

beforeEach(() => {
  globalThis.window = { sessionStorage: new MemoryStorage() }
})

test('same user and scene restore, other users and scenes never reuse progress', () => {
  saveFeedProgress('42', 'following', progress)
  assert.deepEqual(loadFeedProgress('42', 'following'), progress)
  assert.equal(loadFeedProgress('51', 'following'), null)
  assert.equal(loadFeedProgress('42', 'latest'), null)
})

test('refresh clears only the selected user and scene', () => {
  saveFeedProgress('42', 'latest', progress)
  saveFeedProgress('42', 'hot', progress)
  saveFeedProgress('51', 'latest', progress)
  clearFeedProgress('42', 'latest')
  assert.equal(loadFeedProgress('42', 'latest'), null)
  assert.deepEqual(loadFeedProgress('42', 'hot'), progress)
  assert.deepEqual(loadFeedProgress('51', 'latest'), progress)
})

test('logout removes both cache versions and preserves unrelated storage', () => {
  window.sessionStorage.setItem('pulsefeed.feed.v1.latest', '{}')
  window.sessionStorage.setItem('unrelated', 'value')
  saveFeedProgress('42', 'latest', progress)
  saveFeedProgress('51', 'hot', progress)
  clearAllFeedProgress()
  assert.equal(window.sessionStorage.length, 1)
  assert.equal(window.sessionStorage.getItem('unrelated'), 'value')
})

test('malformed cache and absent owner fall back to a fresh request', () => {
  window.sessionStorage.setItem('pulsefeed.feed.v2.42.latest', 'not JSON')
  assert.equal(loadFeedProgress('42', 'latest'), null)
  assert.equal(window.sessionStorage.getItem('pulsefeed.feed.v2.42.latest'), null)
  saveFeedProgress('', 'latest', progress)
  assert.equal(window.sessionStorage.length, 0)
})

test('token subject partitions progress and is independent of token rotation', () => {
  const token = (sub, suffix) => 'header.' + Buffer.from(JSON.stringify({ sub })).toString('base64url') + '.' + suffix
  assert.equal(feedCacheOwner(token('42', 'a')), '42')
  assert.equal(feedCacheOwner(token('42', 'b')), '42')
  assert.equal(feedCacheOwner(token('51', 'a')), '51')
  assert.equal(feedCacheOwner('malformed'), '')
  assert.equal(feedCacheOwner(token('invalid', 'a')), '')
  assert.equal(feedCacheOwner(token(42, 'a')), '')
})

test('account mismatch inside stored record is rejected', () => {
  window.sessionStorage.setItem('pulsefeed.feed.v2.42.latest', JSON.stringify({
    owner: '51', scene: 'latest', version: 2, ...progress,
  }))
  assert.equal(loadFeedProgress('42', 'latest'), null)
})
