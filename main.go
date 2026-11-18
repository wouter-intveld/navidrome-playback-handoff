// Playback Handoff mirrors each user's live playback into their Navidrome play
// queue, so any Subsonic client that restores the server play queue
// (getPlayQueue / getPlayQueueByIndex) continues where another device left off.
package main

import (
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

type plugin struct{}

func init() {
	scrobbler.Register(plugin{})
	scheduler.Register(plugin{})
}

func main() {}

// IsAuthorized accepts every user: Navidrome only sends events for the users an
// admin assigned to this plugin.
func (plugin) IsAuthorized(scrobbler.IsAuthorizedRequest) (bool, error) { return true, nil }

// NowPlaying is covered by PlaybackReport, which Navidrome sends for the same
// events with the playback state and player attached.
func (plugin) NowPlaying(scrobbler.NowPlayingRequest) error { return nil }

func (plugin) Scrobble(scrobbler.ScrobbleRequest) error { return nil }

func (plugin) PlaybackReport(r scrobbler.PlaybackReportRequest) error {
	return handleReport(r, time.Now().UnixMilli())
}

func (plugin) OnCallback(r scheduler.SchedulerCallbackRequest) error {
	return handleTick(r.Payload, time.Now().UnixMilli())
}
