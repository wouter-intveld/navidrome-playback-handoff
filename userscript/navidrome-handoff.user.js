// ==UserScript==
// @name         Navidrome Playback Handoff
// @namespace    https://github.com/wouter-intveld/navidrome-playback-handoff
// @version      0.2.0
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

  // Always read and merge the stored value: another tab, or this page before a
  // reload, may have written since this script started.
  const readMemory = () => JSON.parse(localStorage.getItem(KEY) || '{}')
  const remember = (patch) => localStorage.setItem(KEY, JSON.stringify({ ...readMemory(), ...patch }))

  // A restore chosen before the reload is applied now, before the web UI reads
  // its saved queue from localStorage on startup.
  const pending = sessionStorage.getItem(`${KEY}:restore`)
  if (pending) {
    sessionStorage.removeItem(`${KEY}:restore`)
    const { player, seek, changed } = JSON.parse(pending)
    const state = JSON.parse(localStorage.getItem('state') || '{}')
    state.player = { ...state.player, ...player }
    localStorage.setItem('state', JSON.stringify(state))
    remember({ handled: changed })
    seekWhenLoaded(seek)
  }

  // Media events don't bubble, but a capturing listener on the document sees
  // them for the player's <audio> element. Pausing counts too: the web UI
  // reports the pause, so the server queue changes at that moment.
  for (const type of ['play', 'timeupdate', 'pause', 'seeked', 'ended']) {
    document.addEventListener(
      type,
      (e) => {
        if (!(e.target instanceof HTMLMediaElement)) return
        remember({ lastLocalPlay: Date.now() })
        if (type === 'play') closeBanner()
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

  // The server queue to offer, or null when there is nothing to offer: it was
  // already answered here, or it came from this browser's own playback (the
  // plugin follows every client, this one included).
  const latestOffer = async () => {
    const { playQueue: queue } = await subsonic('getPlayQueue')
    if (!queue?.current || !queue.entry?.some((e) => e.id === queue.current)) return null
    const changed = Date.parse(queue.changed)
    const { handled = 0, lastLocalPlay = 0 } = readMemory()
    if (changed <= handled || changed <= lastLocalPlay + 15000) return null
    return { queue, changed }
  }

  const restore = async ({ queue, changed }) => {
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
        changed,
      }),
    )
    remember({ handled: changed })
    location.reload()
  }

  let banner = null
  let checking = false

  function closeBanner() {
    banner?.root.remove()
    banner = null
  }

  const createBanner = () => {
    const root = document.createElement('div')
    root.setAttribute('role', 'dialog')
    root.setAttribute('aria-label', 'Continue playback from another device')
    root.style.cssText = [
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
    const detail = document.createElement('div')
    detail.style.cssText = 'opacity:.75;white-space:nowrap;overflow:hidden;text-overflow:ellipsis'
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

    // Continue restores the server queue as it is now, not what the banner
    // showed: another device may have moved on since.
    continueButton.addEventListener('click', async () => {
      continueButton.disabled = true
      try {
        const offer = await latestOffer()
        if (offer) await restore(offer)
        else closeBanner()
      } catch (err) {
        detail.textContent = `Could not restore: ${err.message}`
        continueButton.disabled = false
      }
    })
    dismissButton.addEventListener('click', () => {
      remember({ handled: Math.max(banner?.changed || 0, readMemory().handled || 0) })
      closeBanner()
      check()
    })

    root.append(text, continueButton, dismissButton)
    document.body.append(root)
    return { root, title, detail, changed: 0 }
  }

  const showOffer = ({ queue, changed }) => {
    const current = queue.entry.find((e) => e.id === queue.current)
    banner ??= createBanner()
    banner.changed = changed
    banner.title.textContent = `Continue "${current.title}"`
    banner.detail.textContent = [
      current.artist,
      `at ${formatTime((queue.position || 0) / 1000)}`,
      queue.entry.length > 1 ? `${queue.entry.length} tracks in queue` : null,
    ]
      .filter(Boolean)
      .join(' · ')
  }

  // Keep the banner in step with the server queue: show the newest handoff,
  // update it while the other device plays on, and drop it when it no longer
  // applies.
  const check = async () => {
    if (checking || document.hidden || !credentials()) return
    if (isPlayingHere()) return closeBanner()
    checking = true
    try {
      const offer = await latestOffer()
      if (offer && !isPlayingHere()) showOffer(offer)
      else closeBanner()
    } catch (err) {
      console.warn('[playback-handoff]', err)
    } finally {
      checking = false
    }
  }

  const start = () => {
    check()
    document.addEventListener('visibilitychange', check)
    setInterval(check, 15000)
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start)
  } else {
    start()
  }
})()
