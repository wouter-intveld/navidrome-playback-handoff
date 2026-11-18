# Playback Handoff for Navidrome

Spotify-style "continue on another device" for Navidrome. Play on your phone,
open any other client, and it resumes the same track at the same position.

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

If the playing track is already in the saved queue, the queue is kept and only
the current index and position change. Otherwise the queue becomes that single
track, because the playing device never shared its queue.

## Requirements

- A Navidrome version with the playback-report scrobbler capability (built and
  tested against `develop` at `a40f7b87`, 0.64.2-SNAPSHOT).
- On the receiving device, a client that restores the server play queue
  (usually a "sync play queue" / "restore queue from server" option).

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
