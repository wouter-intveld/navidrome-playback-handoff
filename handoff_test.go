package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type fakeHost struct {
	cache     map[string]string
	queueJSON string
	saves     []url.Values
	ticking   map[string]string
	songAlbum map[string]string
	albums    map[string][]string
	lookups   int
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	f := &fakeHost{
		cache:     map[string]string{},
		queueJSON: `{"subsonic-response":{"status":"ok","playQueueByIndex":{"username":"alice"}}}`,
		ticking:   map[string]string{},
		songAlbum: map[string]string{},
		albums:    map[string][]string{},
	}
	for _, m := range []*mock.Mock{&host.CacheMock.Mock, &host.SchedulerMock.Mock, &host.ConfigMock.Mock, &host.SubsonicAPIMock.Mock} {
		m.ExpectedCalls, m.Calls = nil, nil
	}
	pdk.ResetMock()
	pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Return()
	f.songAlbum["t2"] = "al"
	f.albums["al"] = []string{"t1", "t2", "t3"}

	host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil).Run(func(a mock.Arguments) {
		f.cache[a.String(0)] = a.String(1)
	})
	returnsFrom(host.CacheMock.On("GetString", mock.Anything), func(a mock.Arguments) mock.Arguments {
		v, ok := f.cache[a.String(0)]
		return mock.Arguments{v, ok, nil}
	})
	host.CacheMock.On("Remove", mock.Anything).Return(nil).Run(func(a mock.Arguments) {
		delete(f.cache, a.String(0))
	})
	host.SchedulerMock.On("ScheduleRecurring", mock.Anything, mock.Anything, mock.Anything).Return("", nil).Run(func(a mock.Arguments) {
		f.ticking[a.String(2)] = a.String(0)
	})
	host.SchedulerMock.On("CancelSchedule", mock.Anything).Return(nil).Run(func(a mock.Arguments) {
		delete(f.ticking, a.String(0))
	})
	host.ConfigMock.On("GetInt", "refreshSeconds").Return(int64(0), false)
	returnsFrom(host.SubsonicAPIMock.On("Call", mock.Anything), func(a mock.Arguments) mock.Arguments {
		uri := a.String(0)
		endpoint, query, _ := strings.Cut(uri, "?")
		switch endpoint {
		case "getPlayQueueByIndex":
			return mock.Arguments{f.queueJSON, nil}
		case "savePlayQueueByIndex":
			q, err := url.ParseQuery(query)
			if err != nil {
				return mock.Arguments{"", err}
			}
			f.saves = append(f.saves, q)
			return mock.Arguments{`{"subsonic-response":{"status":"ok"}}`, nil}
		case "getSong":
			f.lookups++
			q, _ := url.ParseQuery(query)
			albumID, ok := f.songAlbum[q.Get("id")]
			if !ok {
				return mock.Arguments{`{"subsonic-response":{"status":"failed","error":{"code":70,"message":"not found"}}}`, nil}
			}
			return mock.Arguments{`{"subsonic-response":{"status":"ok","song":{"id":"` + q.Get("id") + `","albumId":"` + albumID + `"}}}`, nil}
		case "getAlbum":
			q, _ := url.ParseQuery(query)
			var songs []string
			for _, id := range f.albums[q.Get("id")] {
				songs = append(songs, `{"id":"`+id+`"}`)
			}
			return mock.Arguments{`{"subsonic-response":{"status":"ok","album":{"song":[` + strings.Join(songs, ",") + `]}}}`, nil}
		}
		return mock.Arguments{"", errors.New("unexpected call " + uri)}
	})
	return f
}

// returnsFrom computes a mock's return values per call; testify reads
// ReturnArguments after running the Run hook.
func returnsFrom(c *mock.Call, fn func(mock.Arguments) mock.Arguments) {
	c.Run(func(a mock.Arguments) { c.ReturnArguments = fn(a) })
}

func (f *fakeHost) lastSave(t *testing.T) url.Values {
	t.Helper()
	require.NotEmpty(t, f.saves, "expected a savePlayQueueByIndex call")
	return f.saves[len(f.saves)-1]
}

func report(state string, positionMs int64) scrobbler.PlaybackReportRequest {
	return scrobbler.PlaybackReportRequest{
		Username:     "alice",
		Track:        scrobbler.TrackInfo{ID: "t2", Title: "Song", Duration: 200},
		State:        state,
		PositionMs:   positionMs,
		PlaybackRate: 1,
		PlayerName:   "Phone",
	}
}

func TestPlayingTrackOutsideQueueBecomesTheQueue(t *testing.T) {
	f := newFakeHost(t)

	require.NoError(t, handleReport(report("playing", 42_000), 1_000))

	save := f.lastSave(t)
	assert.Equal(t, []string{"t2"}, save["id"])
	assert.Equal(t, "0", save.Get("currentIndex"))
	assert.Equal(t, "42000", save.Get("position"))
	assert.Equal(t, "alice", save.Get("u"))
	assert.Equal(t, "@every 10s", f.ticking["tick:alice"])
}

func TestPlayingTrackInsideQueueKeepsTheQueue(t *testing.T) {
	f := newFakeHost(t)
	f.queueJSON = `{"subsonic-response":{"status":"ok","playQueueByIndex":{"entry":[{"id":"t1"},{"id":"t2"},{"id":"t3"}],"currentIndex":0}}}`

	require.NoError(t, handleReport(report("starting", 0), 1_000))

	save := f.lastSave(t)
	assert.Equal(t, []string{"t1", "t2", "t3"}, save["id"])
	assert.Equal(t, "1", save.Get("currentIndex"))
}

func TestPauseSavesPositionAndStopsTicking(t *testing.T) {
	f := newFakeHost(t)
	require.NoError(t, handleReport(report("playing", 10_000), 1_000))

	require.NoError(t, handleReport(report("paused", 61_500), 50_000))

	assert.Equal(t, "61500", f.lastSave(t).Get("position"))
	assert.Empty(t, f.ticking)
}

func TestTickAdvancesPositionOfTrackReportedOnlyAtStart(t *testing.T) {
	f := newFakeHost(t)
	require.NoError(t, handleReport(report("playing", 0), 1_000))

	require.NoError(t, handleTick("alice", 16_000))

	assert.Equal(t, "15000", f.lastSave(t).Get("position"))
}

func TestTickHonoursPlaybackRate(t *testing.T) {
	f := newFakeHost(t)
	r := report("playing", 0)
	r.PlaybackRate = 1.5
	require.NoError(t, handleReport(r, 0))

	require.NoError(t, handleTick("alice", 10_000))

	assert.Equal(t, "15000", f.lastSave(t).Get("position"))
}

func TestTickPastEndOfTrackEndsSessionWithoutSaving(t *testing.T) {
	f := newFakeHost(t)
	require.NoError(t, handleReport(report("playing", 190_000), 0))
	saves := len(f.saves)

	require.NoError(t, handleTick("alice", 20_000))

	assert.Len(t, f.saves, saves)
	assert.Empty(t, f.ticking)
	assert.Empty(t, f.cache)
}

func TestTickWithoutSessionCancelsSchedule(t *testing.T) {
	f := newFakeHost(t)
	f.ticking["tick:alice"] = "@every 10s"

	require.NoError(t, handleTick("alice", 0))

	assert.Empty(t, f.saves)
	assert.Empty(t, f.ticking)
}

func TestStopMidTrackSavesPosition(t *testing.T) {
	f := newFakeHost(t)

	require.NoError(t, handleReport(report("stopped", 80_000), 0))

	assert.Equal(t, "80000", f.lastSave(t).Get("position"))
	assert.Empty(t, f.cache)
}

func TestStopAtEndOfTrackSavesNothing(t *testing.T) {
	f := newFakeHost(t)

	require.NoError(t, handleReport(report("stopped", 199_500), 0))

	assert.Empty(t, f.saves)
}

func TestExpiredSessionKeepsLastSavedQueue(t *testing.T) {
	f := newFakeHost(t)
	require.NoError(t, handleReport(report("playing", 5_000), 0))
	saves := len(f.saves)

	require.NoError(t, handleReport(report("expired", 5_000), 300_000))

	assert.Len(t, f.saves, saves)
	assert.Empty(t, f.ticking)
	assert.Empty(t, f.cache)
}

func TestConfiguredRefreshInterval(t *testing.T) {
	f := newFakeHost(t)
	host.ConfigMock.ExpectedCalls = nil
	host.ConfigMock.On("GetInt", "refreshSeconds").Return(int64(30), true)

	require.NoError(t, handleReport(report("playing", 0), 0))

	assert.Equal(t, "@every 30s", f.ticking["tick:alice"])
}

func TestSubsonicFailureIsReported(t *testing.T) {
	f := newFakeHost(t)
	f.queueJSON = `{"subsonic-response":{"status":"failed","error":{"code":50,"message":"not authorized"}}}`

	err := handleReport(report("playing", 0), 0)

	require.ErrorContains(t, err, "not authorized")
	assert.Empty(t, f.saves)
}

func TestNextTrackOfAlbumExpandsQueueToAlbum(t *testing.T) {
	f := newFakeHost(t)
	f.queueJSON = `{"subsonic-response":{"status":"ok","playQueueByIndex":{"entry":[{"id":"t1"}],"currentIndex":0}}}`

	require.NoError(t, handleReport(report("playing", 0), 0))

	save := f.lastSave(t)
	assert.Equal(t, []string{"t1", "t2", "t3"}, save["id"])
	assert.Equal(t, "1", save.Get("currentIndex"))
}

func TestTrackNotFollowingPreviousOneStaysSingle(t *testing.T) {
	f := newFakeHost(t)
	f.queueJSON = `{"subsonic-response":{"status":"ok","playQueueByIndex":{"entry":[{"id":"t3"}],"currentIndex":0}}}`

	require.NoError(t, handleReport(report("playing", 0), 0))

	assert.Equal(t, []string{"t2"}, f.lastSave(t)["id"])
}

func TestFirstTrackWithoutPreviousSkipsAlbumLookup(t *testing.T) {
	f := newFakeHost(t)

	require.NoError(t, handleReport(report("playing", 0), 0))

	assert.Equal(t, []string{"t2"}, f.lastSave(t)["id"])
	assert.Zero(t, f.lookups)
}

func TestAlbumLookupFailureStillSavesTrack(t *testing.T) {
	f := newFakeHost(t)
	f.queueJSON = `{"subsonic-response":{"status":"ok","playQueueByIndex":{"entry":[{"id":"t1"}],"currentIndex":0}}}`
	delete(f.songAlbum, "t2")

	require.NoError(t, handleReport(report("playing", 0), 0))

	assert.Equal(t, []string{"t2"}, f.lastSave(t)["id"])
}

func TestIndexInQueue(t *testing.T) {
	cases := []struct {
		name      string
		ids       []string
		current   int
		wantIndex int
		wantFound bool
	}{
		{"current entry", []string{"a", "t", "b"}, 1, 1, true},
		{"next occurrence after current", []string{"t", "a", "t"}, 1, 2, true},
		{"earlier occurrence", []string{"t", "a", "b"}, 2, 0, true},
		{"not in queue", []string{"a", "b"}, 0, 0, false},
		{"empty queue", nil, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, found := indexInQueue(c.ids, c.current, "t")
			assert.Equal(t, c.wantIndex, idx)
			assert.Equal(t, c.wantFound, found)
		})
	}
}
