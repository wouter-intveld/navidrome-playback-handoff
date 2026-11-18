// ==UserScript==
// @name         Navidrome Playback Handoff
// @namespace    https://github.com/wouter-intveld/navidrome-playback-handoff
// @version      0.1.0
// @description  Continue in the Navidrome web UI where another device left off. Needs the playback-handoff server plugin.
// @homepageURL  https://github.com/wouter-intveld/navidrome-playback-handoff
// @downloadURL  https://raw.githubusercontent.com/wouter-intveld/navidrome-playback-handoff/main/userscript/navidrome-handoff.user.js
// @updateURL    https://raw.githubusercontent.com/wouter-intveld/navidrome-playback-handoff/main/userscript/navidrome-handoff.user.js
// @match        *://*/app/*
// @match        *://*/*/app/*
// @grant        none
// @run-at       document-start
// ==/UserScript==

// Also works without a userscript manager: a reverse proxy can add
// <script src="..."></script> to Navidrome's index.html (see README).
;(() => {
  'use strict'

  const KEY = 'playback-handoff'
  const appAt = location.pathname.indexOf('/app/')
  if (appAt < 0) return
  const base = location.pathname.slice(0, appAt)

  const memory = JSON.parse(localStorage.getItem(KEY) || '{}')
  const remember = (patch) => {
    Object.assign(memory, patch)
    localStorage.setItem(KEY, JSON.stringify(memory))
  }

  // A restore chosen before the reload is applied now, before the web UI reads
  // its saved queue from localStorage on startup.
  const pending = sessionStorage.getItem(`${KEY}:restore`)
  if (pending) {
    sessionStorage.removeItem(`${KEY}:restore`)
    const { player, seek } = JSON.parse(pending)
    const state = JSON.parse(localStorage.getItem('state') || '{}')
    state.player = { ...state.player, ...player }
    localStorage.setItem('state', JSON.stringify(state))
    seekWhenLoaded(seek)
  }

  // Media events don't bubble, but a capturing listener on the document sees
  // them for the player's <audio> element. Pausing counts too: the web UI
  // reports the pause, so the server queue changes at that moment.
  for (const type of ['play', 'timeupdate', 'pause', 'seeked', 'ended']) {
    document.addEventListener(
      type,
      (e) => {
        if (e.target instanceof HTMLMediaElement) remember({ lastLocalPlay: Date.now() })
      },
      true,
    )
  }

  const credentials = () => {
    const u = localStorage.getItem('username')
    const t = localStorage.getItem('subsonic-token')
    const s = localStorage.getItem('subsonic-salt')
    return u && t && s ? { u, t, s } : null
  }

  const subsonicURL = (command, params = {}) => {
    const query = new URLSearchParams({
      ...credentials(),
      v: '1.16.1',
      c: 'NavidromeUI',
      f: 'json',
      ...params,
    })
    return `${base}/rest/${command}?${query}`
  }

  const subsonic = async (command, params) => {
    const res = await fetch(subsonicURL(command, params))
    const body = (await res.json())['subsonic-response']
    if (body.status !== 'ok') {
      throw new Error(`${command}: ${body.error?.message || body.status}`)
    }
    return body
  }

  // The web UI's queue items hold Navidrome's native song records, so fetch
  // those rather than translating Subsonic entries.
  const songRecords = async (ids) => {
    const records = new Map()
    for (let i = 0; i < ids.length; i += 100) {
      const chunk = ids.slice(i, i + 100)
      const query = chunk.map((id) => `id=${encodeURIComponent(id)}`).join('&')
      const res = await fetch(`${base}/api/song?${query}&_start=0&_end=${chunk.length}`, {
        headers: { 'X-ND-Authorization': `Bearer ${localStorage.getItem('token')}` },
      })
      if (!res.ok) throw new Error(`loading songs: HTTP ${res.status}`)
      for (const song of await res.json()) records.set(song.id, song)
    }
    return records
  }

  const uuid = () =>
    crypto.randomUUID?.() ??
    'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
      const r = (Math.random() * 16) | 0
      return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16)
    })

  // Mirrors mapToAudioLists in Navidrome's playerReducer. The player rebuilds
  // musicSrc on startup, but it can load the current item before that, so it
  // needs a playable URL: the same plain stream URL the web UI falls back to.
  const queueItem = (song) => ({
    trackId: song.id,
    uuid: uuid(),
    song,
    name: song.title,
    lyric: '',
    singer: song.artist,
    duration: song.duration,
    musicSrc: subsonicURL('stream', { id: song.id, _: Date.now() }),
    cover: subsonicURL('getCoverArt', { id: song.id, size: 300, _: song.updatedAt }),
  })

  function seekWhenLoaded({ trackId, seconds }) {
    if (!seconds) return
    const onMetadata = (e) => {
      const audio = e.target
      if (!(audio instanceof HTMLMediaElement) || !audio.currentSrc.includes(trackId)) return
      document.removeEventListener('loadedmetadata', onMetadata, true)
      audio.currentTime = seconds
    }
    document.addEventListener('loadedmetadata', onMetadata, true)
  }

  const isPlayingHere = () => [...document.querySelectorAll('audio, video')].some((m) => !m.paused)

  const formatTime = (seconds) => {
    const s = Math.floor(seconds)
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
  }

  let banner = null
  let checking = false

  const closeBanner = () => {
    banner?.remove()
    banner = null
  }

  const restore = async (queue, changed) => {
    const ids = queue.entry.map((e) => e.id)
    const records = await songRecords(ids)
    const items = ids.filter((id) => records.has(id)).map((id) => queueItem(records.get(id)))
    const index = items.findIndex((item) => item.trackId === queue.current)
    if (index < 0) throw new Error('current track is not available in this library')
    sessionStorage.setItem(
      `${KEY}:restore`,
      JSON.stringify({
        player: { queue: items, savedPlayIndex: index },
        seek: { trackId: queue.current, seconds: (queue.position || 0) / 1000 },
      }),
    )
    remember({ handled: changed })
    location.reload()
  }

  const showBanner = (queue, changed) => {
    const current = queue.entry.find((e) => e.id === queue.current)
    const position = (queue.position || 0) / 1000

    banner = document.createElement('div')
    banner.setAttribute('role', 'dialog')
    banner.setAttribute('aria-label', 'Continue playback from another device')
    banner.style.cssText = [
      'position:fixed', 'left:50%', 'bottom:96px', 'transform:translateX(-50%)',
      'z-index:2000', 'display:flex', 'align-items:center', 'gap:12px',
      'max-width:calc(100vw - 32px)', 'padding:12px 16px', 'border-radius:8px',
      'background:#222', 'color:#fff', 'font:14px/1.4 system-ui,sans-serif',
      'box-shadow:0 4px 16px rgba(0,0,0,.4)',
    ].join(';')

    const text = document.createElement('div')
    text.style.cssText = 'min-width:0;overflow:hidden'
    const title = document.createElement('div')
    title.style.cssText = 'font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis'
    title.textContent = `Continue "${current.title}"`
    const detail = document.createElement('div')
    detail.style.cssText = 'opacity:.75;white-space:nowrap;overflow:hidden;text-overflow:ellipsis'
    detail.textContent = [
      current.artist,
      `at ${formatTime(position)}`,
      queue.entry.length > 1 ? `${queue.entry.length} tracks in queue` : null,
    ]
      .filter(Boolean)
      .join(' · ')
    text.append(title, detail)

    const button = (label, primary) => {
      const b = document.createElement('button')
      b.type = 'button'
      b.textContent = label
      b.style.cssText = [
        'flex:none', 'padding:6px 12px', 'border-radius:4px', 'cursor:pointer',
        'font:inherit', 'border:1px solid #555',
        primary ? 'background:#3f51b5;color:#fff;border-color:#3f51b5' : 'background:transparent;color:#fff',
      ].join(';')
      return b
    }
    const continueButton = button('Continue', true)
    const dismissButton = button('Not now', false)
    continueButton.addEventListener('click', async () => {
      continueButton.disabled = true
      try {
        await restore(queue, changed)
      } catch (err) {
        detail.textContent = `Could not restore: ${err.message}`
        continueButton.disabled = false
      }
    })
    dismissButton.addEventListener('click', () => {
      remember({ handled: changed })
      closeBanner()
    })

    banner.append(text, continueButton, dismissButton)
    document.body.append(banner)
  }

  // Offer the server queue when another device changed it after this browser
  // last played or answered an offer.
  const check = async () => {
    if (checking || banner || document.hidden || !credentials() || isPlayingHere()) return
    checking = true
    try {
      const { playQueue: queue } = await subsonic('getPlayQueue')
      if (!queue?.current || !queue.entry?.length) return
      const changed = Date.parse(queue.changed)
      if (changed <= (memory.handled || 0)) return
      // This browser's own playback also updates the server queue (the plugin
      // follows every client), so ignore changes made while it was playing.
      if (changed <= (memory.lastLocalPlay || 0) + 15000) return
      showBanner(queue, changed)
    } catch (err) {
      console.warn('[playback-handoff]', err)
    } finally {
      checking = false
    }
  }

  const start = () => {
    check()
    document.addEventListener('visibilitychange', check)
    setInterval(check, 30000)
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start)
  } else {
    start()
  }
})()
