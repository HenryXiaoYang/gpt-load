package proxy

import (
	"sync"
	"time"

	"gpt-load/internal/models"
)

const (
	channelRPMWindow           = time.Minute
	channelRPMDefaultThreshold = 60
	channelRPMDefaultRamp      = 5 * time.Minute
)

type channelRPMConfig struct {
	limit            int
	thresholdPercent int
	ramp             time.Duration
}

type channelRPMState struct {
	requests      []int64
	protectionCap int
	protectionAt  int64
}

// ponytail: one process-wide lock keeps the limiter small; use the existing store's
// Redis primitives if multi-instance RPM coordination becomes a requirement.
var channelRPMMemory = struct {
	sync.Mutex
	groups map[uint]*channelRPMState
}{groups: make(map[uint]*channelRPMState)}

func channelRPMConfigForGroup(group *models.Group) (channelRPMConfig, bool) {
	if group == nil || group.ID == 0 || group.EffectiveConfig.ChannelRPMLimit <= 0 {
		return channelRPMConfig{}, false
	}
	threshold := group.EffectiveConfig.ChannelRPMThresholdPercent
	if threshold <= 0 {
		threshold = channelRPMDefaultThreshold
	}
	if threshold > 100 {
		threshold = 100
	}
	ramp := time.Duration(group.EffectiveConfig.ChannelRPMRampMinutes) * time.Minute
	if ramp <= 0 {
		ramp = channelRPMDefaultRamp
	}
	return channelRPMConfig{
		limit:            group.EffectiveConfig.ChannelRPMLimit,
		thresholdPercent: threshold,
		ramp:             ramp,
	}, true
}

func channelRPMEffectiveLimit(cfg channelRPMConfig, state *channelRPMState, now int64) int {
	if state.protectionCap <= 0 || state.protectionAt <= 0 {
		return cfg.limit
	}
	elapsed := time.Duration(now-state.protectionAt) * time.Millisecond
	if elapsed >= cfg.ramp {
		return cfg.limit
	}
	if elapsed < 0 {
		elapsed = 0
	}
	start := state.protectionCap
	if start < 1 {
		start = 1
	}
	if start > cfg.limit {
		start = cfg.limit
	}
	return max(1, min(cfg.limit, start+int(float64(cfg.limit-start)*float64(elapsed)/float64(cfg.ramp))))
}

func pruneChannelRPMRequests(requests []int64, now int64) []int64 {
	cutoff := now - channelRPMWindow.Milliseconds()
	first := 0
	for first < len(requests) && requests[first] <= cutoff {
		first++
	}
	if first == 0 {
		return requests
	}
	return append([]int64(nil), requests[first:]...)
}

func tryAcquireChannelRPM(group *models.Group) bool {
	cfg, enabled := channelRPMConfigForGroup(group)
	if !enabled {
		return true
	}
	return tryAcquireChannelRPMAt(group.ID, cfg, time.Now().UnixMilli())
}

func tryAcquireChannelRPMAt(groupID uint, cfg channelRPMConfig, now int64) bool {
	channelRPMMemory.Lock()
	defer channelRPMMemory.Unlock()
	state := channelRPMMemory.groups[groupID]
	if state == nil {
		state = &channelRPMState{}
		channelRPMMemory.groups[groupID] = state
	}
	state.requests = pruneChannelRPMRequests(state.requests, now)
	effective := channelRPMEffectiveLimit(cfg, state, now)
	if effective == cfg.limit && state.protectionCap > 0 {
		state.protectionCap = 0
		state.protectionAt = 0
	}
	if len(state.requests) >= effective {
		return false
	}
	state.requests = append(state.requests, now)
	return true
}

func recordChannelRPM429(group *models.Group) {
	cfg, enabled := channelRPMConfigForGroup(group)
	if !enabled {
		return
	}
	now := time.Now().UnixMilli()
	channelRPMMemory.Lock()
	defer channelRPMMemory.Unlock()
	state := channelRPMMemory.groups[group.ID]
	if state == nil {
		state = &channelRPMState{}
		channelRPMMemory.groups[group.ID] = state
	}
	state.requests = pruneChannelRPMRequests(state.requests, now)
	current := len(state.requests)
	reduced := current * cfg.thresholdPercent / 100
	if reduced < 1 {
		reduced = 1
	}
	if reduced > cfg.limit {
		reduced = cfg.limit
	}
	effective := channelRPMEffectiveLimit(cfg, state, now)
	if reduced > effective {
		reduced = effective
	}
	state.protectionCap = reduced
	state.protectionAt = now
}

func resetChannelRPMState(groupID uint) {
	channelRPMMemory.Lock()
	delete(channelRPMMemory.groups, groupID)
	channelRPMMemory.Unlock()
}
