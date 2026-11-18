package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const (
	stateStarting = "starting"
	statePlaying  = "playing"
	statePaused   = "paused"
	stateStopped  = "stopped"
	stateExpired  = "expired"

	defaultRefreshSeconds = 10
	sessionTTLSeconds     = 6 * 60 * 60
	// A stop this close to the end of a track means it finished rather than
	// being interrupted, so there is nothing to resume.
	finishedSlackMs = 2000
)

type session struct {
	TrackID      string  `json:"trackId"`
	DurationMs   int64   `json:"durationMs"`
	PositionMs   int64   `json:"positionMs"`
	Rate         float64 `json:"rate"`
	State        string  `json:"state"`
	Player       string  `json:"player"`
	ReportedAtMs int64   `json:"reportedAtMs"`
}

func (s session) running() bool { return s.State == statePlaying || s.State == stateStarting }

// positionAt estimates the playback position at nowMs, advancing the clock only
// while the track is running.
func (s session) positionAt(nowMs int64) int64 {
	if !s.running() {
		return s.PositionMs
	}
	rate := s.Rate
	if rate <= 0 {
		rate = 1
	}
	pos := s.PositionMs + int64(float64(nowMs-s.ReportedAtMs)*rate)
	return min(pos, s.DurationMs)
}

func handleReport(r scrobbler.PlaybackReportRequest, nowMs int64) error {
	user := r.Username
	s := session{
		TrackID:      r.Track.ID,
		DurationMs:   int64(r.Track.Duration * 1000),
		PositionMs:   r.PositionMs,
		Rate:         r.PlaybackRate,
		State:        r.State,
		Player:       r.PlayerName,
		ReportedAtMs: nowMs,
	}
	pdk.Log(pdk.LogDebug, fmt.Sprintf("handoff: %s %s %q at %dms on %q", user, s.State, r.Track.Title, s.PositionMs, s.Player))

	switch s.State {
	case stateStarting, statePlaying, statePaused:
		if err := storeSession(user, s); err != nil {
			return err
		}
		if s.running() {
			if err := startTicking(user); err != nil {
				return err
			}
		} else {
			stopTicking(user)
		}
		return saveQueue(user, s.TrackID, s.PositionMs)

	case stateStopped:
		if err := endSession(user); err != nil {
			return err
		}
		if s.PositionMs >= s.DurationMs-finishedSlackMs {
			return nil
		}
		return saveQueue(user, s.TrackID, s.PositionMs)

	case stateExpired:
		// The player went silent: keep the last saved position rather than
		// guessing how far an unreported pause or skip got.
		return endSession(user)
	}
	return nil
}

// handleTick advances the saved position of a running track, so clients that
// only report track starts still hand off close to the real position.
func handleTick(user string, nowMs int64) error {
	s, ok, err := loadSession(user)
	if err != nil {
		return err
	}
	if !ok || !s.running() {
		stopTicking(user)
		return nil
	}
	pos := s.positionAt(nowMs)
	if pos >= s.DurationMs {
		// The track ran out without a new report: playback stopped or the next
		// track's report is on its way. Either way there is nothing to advance.
		return endSession(user)
	}
	return saveQueue(user, s.TrackID, pos)
}

func sessionKey(user string) string { return "session:" + user }
func scheduleID(user string) string { return "tick:" + user }

func storeSession(user string, s session) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return host.CacheSetString(sessionKey(user), string(b), sessionTTLSeconds)
}

func loadSession(user string) (session, bool, error) {
	var s session
	raw, ok, err := host.CacheGetString(sessionKey(user))
	if err != nil || !ok {
		return s, false, err
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, false, err
	}
	return s, true, nil
}

func endSession(user string) error {
	stopTicking(user)
	return host.CacheRemove(sessionKey(user))
}

// startTicking (re)creates the user's recurring position refresh. Recreating it
// on every report keeps the interval aligned with the latest known position.
func startTicking(user string) error {
	stopTicking(user)
	_, err := host.SchedulerScheduleRecurring(fmt.Sprintf("@every %ds", refreshSeconds()), user, scheduleID(user))
	return err
}

func stopTicking(user string) {
	// Fails only when no schedule exists for the user, which is the state we want.
	_ = host.SchedulerCancelSchedule(scheduleID(user))
}

func refreshSeconds() int64 {
	if v, ok := host.ConfigGetInt("refreshSeconds"); ok && v >= 5 {
		return v
	}
	return defaultRefreshSeconds
}

type subsonicResponse struct {
	Response struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		PlayQueueByIndex *struct {
			Entry []struct {
				ID string `json:"id"`
			} `json:"entry"`
			CurrentIndex *int `json:"currentIndex"`
		} `json:"playQueueByIndex"`
	} `json:"subsonic-response"`
}

func callSubsonic(uri string) (subsonicResponse, error) {
	var resp subsonicResponse
	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return resp, err
	}
	if resp.Response.Status != "ok" {
		if e := resp.Response.Error; e != nil {
			return resp, fmt.Errorf("subsonic error %d: %s", e.Code, e.Message)
		}
		return resp, errors.New("subsonic call failed: " + raw)
	}
	return resp, nil
}

// saveQueue points the user's server play queue at trackID/positionMs, keeping
// the existing queue when it contains the track.
func saveQueue(user, trackID string, positionMs int64) error {
	resp, err := callSubsonic("getPlayQueueByIndex?u=" + url.QueryEscape(user))
	if err != nil {
		return fmt.Errorf("reading play queue of %s: %w", user, err)
	}
	var ids []string
	current := 0
	if pq := resp.Response.PlayQueueByIndex; pq != nil {
		for _, e := range pq.Entry {
			ids = append(ids, e.ID)
		}
		if pq.CurrentIndex != nil {
			current = *pq.CurrentIndex
		}
	}
	ids, current = placeInQueue(ids, current, trackID)

	q := url.Values{}
	q.Set("u", user)
	q["id"] = ids
	q.Set("currentIndex", strconv.Itoa(current))
	q.Set("position", strconv.FormatInt(max(positionMs, 0), 10))
	if _, err := callSubsonic("savePlayQueueByIndex?" + q.Encode()); err != nil {
		return fmt.Errorf("saving play queue of %s: %w", user, err)
	}
	return nil
}

// placeInQueue returns the queue and index to save for trackID. It prefers the
// current entry, then the next occurrence after it (the player advanced), then
// any occurrence; a track outside the queue replaces it, since the device that
// is playing it never shared its queue.
func placeInQueue(ids []string, current int, trackID string) ([]string, int) {
	if current >= 0 && current < len(ids) && ids[current] == trackID {
		return ids, current
	}
	for i := max(current+1, 0); i < len(ids); i++ {
		if ids[i] == trackID {
			return ids, i
		}
	}
	for i, id := range ids {
		if id == trackID {
			return ids, i
		}
	}
	return []string{trackID}, 0
}
