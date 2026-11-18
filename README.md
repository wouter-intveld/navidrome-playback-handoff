# Playback Handoff for Navidrome

Spotify-style "continue on another device" for Navidrome. Play on your phone,
open any other client, and it resumes the same track at the same position.

> **Browsers need the companion script.** The plugin only keeps the server play
> queue current; the client you switch to has to load it. The Navidrome web UI
> never does, so handing off to a browser needs
> [`navidrome-handoff.user.js`](userscript/navidrome-handoff.user.js),
> installed as a userscript or injected by your reverse proxy. See
> [Web UI (browsers)](#web-ui-browsers).

## How it works

Navidrome has a server-side play queue per user (`savePlayQueue` /
`getPlayQueue` in the Subsonic API). Many clients already restore it on
startup or on demand. What's missing is a play queue that is always current, no
matter which client was playing.

This plugin keeps it current. It registers as a scrobbler and receives
Navidrome's playback reports, which every client produces:

- legacy Subsonic now-playing calls (`scrobble?submission=false`),
- OpenSubsonic `reportPlayback` (playing / paused / stopped with position),
- Jellyfin-API clients (`/Sessions/Playing*`).

On each report it updates the user's play queue through the internal Subsonic
API:

| Report | What the plugin saves |
| --- | --- |
| `starting` / `playing` | Track and position, and starts a refresh timer |
| `paused` | Track and exact position, and stops the timer |
| `stopped` mid-track | Track and position (nothing if the track had finished) |
| `expired` | Nothing; keeps the last saved state |

While a track plays, the timer (every `refreshSeconds`, default 10) moves the
saved position forward using the elapsed time and playback rate. So clients
that only report the start of each track still hand off close to where you
were.

### Which queue gets saved

The plugin sees the playing track, not the client's queue, so:

- If the track is already in the saved queue (for example because the client
  uploads its own queue, like Feishin with auto-save), that queue is kept and
  only the current track and position change.
- If the track directly follows the previously playing track on its album, you
  are playing through that album, so the queue becomes the whole album,
  positioned at this track.
- Anything else (playlists, shuffle, a single pick, or the first track you
  start) is saved as a single-track queue rather than a wrong guess.

So after the second track of an album, the receiving device gets the rest of
the album. Playlists and shuffled queues only hand off in full from clients
that upload their own queue.

## Requirements

- A Navidrome version with the playback-report scrobbler capability (built and
  tested against `develop` at `a40f7b87`, 0.64.2-SNAPSHOT).
- On the receiving device, a client that loads the server play queue (see
  below).

## Client support

Every client sends playback, so any client can be the device you hand off
*from*. The device you hand off *to* must load the server play queue:

- **Feishin:** yes, with the "Restore queue from server" button in the queue
  panel. It restores the track and position.
- **Navidrome web UI (desktop and mobile browsers):** only with the companion
  script below. On its own it keeps its queue in the browser and never loads
  the server queue.
- **Amperfy (iOS):** no. It has no play-queue sync.
- **Other Subsonic clients:** yes, if they have a "sync" or "restore play
  queue" option.

A Navidrome plugin can't add this to the web UI itself: plugins have no access
to the web interface and can't serve pages.

## Web UI (browsers)

[`userscript/navidrome-handoff.user.js`](userscript/navidrome-handoff.user.js)
adds handoff to the Navidrome web UI. When another device changed your play
queue since this browser last played, it shows a banner above the player:
"Continue *track* at *position* · *n* tracks in queue". **Continue** loads that
queue into the web UI (it reloads the page once) and starts the track at the
saved position when you press play. **Not now** hides it until the next
handoff.

It uses the web UI's own login, needs no configuration, never offers a
browser its own playback back, and checks again when you return to the tab.
Install it one of two ways.

### Option A: userscript (per browser)

1. Install a userscript manager:
   - Desktop (Firefox, Chrome, Edge): [Violentmonkey](https://violentmonkey.github.io/)
     or Tampermonkey.
   - iPhone/iPad: the free "Userscripts" Safari extension, enabled in
     Settings > Safari > Extensions. It only runs in Safari tabs, not in a
     home-screen web app.
   - Android: Firefox with Violentmonkey.
2. Open the
   [raw script](https://raw.githubusercontent.com/wouter-intveld/navidrome-playback-handoff/main/userscript/navidrome-handoff.user.js)
   and confirm the install. Updates arrive through the manager.

The script matches any URL with `/app/` in the path and does nothing unless
that page is a logged-in Navidrome. To limit it to your server, add a match
rule such as `https://music.example.com/app/*` in the manager.

### Option B: inject it with your reverse proxy (all browsers, no extension)

The proxy adds `<script src="/navidrome-handoff.user.js"></script>` to
Navidrome's page, so every browser, including phones and home-screen web
apps, gets it without installing anything.

With **Caddy**, body rewriting needs the
[`replace-response`](https://github.com/caddyserver/replace-response) module:

```sh
xcaddy build --with github.com/caddyserver/replace-response
# or, for an existing binary:
caddy add-package github.com/caddyserver/replace-response
```

Put `navidrome-handoff.user.js` in a directory Caddy can read (here
`/srv/navidrome-handoff`) and extend your site block:

```caddyfile
{
    order replace after encode
}

music.example.com {
    handle /navidrome-handoff.user.js {
        root * /srv/navidrome-handoff
        file_server
    }
    handle {
        reverse_proxy navidrome:4533 {
            # replace-response can't rewrite compressed bodies
            header_up Accept-Encoding identity
        }
        replace {
            match {
                header Content-Type text/html*
            }
            </head> "<script src=\"/navidrome-handoff.user.js\"></script></head>"
        }
    }
}
```

If Navidrome runs under a `BaseURL` such as `/music`, serve the script under
that prefix and use the same path in the `<script>` tag. Don't use both
options in one browser, or the banner shows twice.

## Build

```sh
make            # TinyGo via the tinygo/tinygo Docker image -> playback-handoff.ndp
make test       # unit tests (native Go, PDK host mocks)
```

## Install

```toml
[Plugins]
Enabled = true
```

1. Download `playback-handoff.ndp` from the
   [latest release](https://github.com/wouter-intveld/navidrome-playback-handoff/releases/latest)
   (or build it) and copy it into the plugins folder (default
   `DataFolder/plugins`).
2. In the UI's Plugins page, or with the CLI, grant users and enable it:

   ```sh
   navidrome plugin edit playback-handoff --all-users   # or --users alice,bob
   navidrome plugin enable playback-handoff
   ```

Replacing the `.ndp` file disables the plugin, so re-enable it after upgrading.

## Releasing

Set `version` in `manifest.json`, commit, then push a matching tag:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

CI refuses a tag that doesn't match the manifest version, builds the plugin and
attaches `playback-handoff.ndp` to a GitHub release.

## Limits

- Subsonic has no way to push to clients, so this is pull-based. The second
  device picks up when it reads the play queue. It can't take over a device
  that is already playing, and it can't pause the first device (Spotify Connect
  can).
- Pauses are only seen from clients that report them (OpenSubsonic
  `reportPlayback`, Jellyfin API). With legacy clients the saved position keeps
  advancing during a pause until the track's expected end.
- The queue's `changedBy` shows `playback-handoff`, because Navidrome sets the
  client name for plugin API calls.
- If a client saves its own queue at the same moment as a plugin update, the
  last write wins.
