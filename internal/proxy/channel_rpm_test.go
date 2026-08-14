package proxy

import (
	"testing"
	"time"
)

func TestChannelRPMProtection(t *testing.T) {
	groupID := uint(987654321)
	resetChannelRPMState(groupID)
	cfg := channelRPMConfig{limit: 2, thresholdPercent: 60, ramp: time.Minute}
	now := time.Now().UnixMilli()
	if !tryAcquireChannelRPMAt(groupID, cfg, now) || !tryAcquireChannelRPMAt(groupID, cfg, now+1) {
		t.Fatal("expected the first two requests to be allowed")
	}
	if tryAcquireChannelRPMAt(groupID, cfg, now+2) {
		t.Fatal("expected the configured RPM limit to reject the third request")
	}
	resetChannelRPMState(groupID)
}

func TestChannelRPMProtectionRampsDownAfter429(t *testing.T) {
	groupID := uint(987654322)
	resetChannelRPMState(groupID)
	cfg := channelRPMConfig{limit: 10, thresholdPercent: 60, ramp: time.Minute}
	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		if !tryAcquireChannelRPMAt(groupID, cfg, now+int64(i)) {
			t.Fatalf("request %d was unexpectedly rejected", i)
		}
	}

	state := channelRPMMemory.groups[groupID]
	if state == nil {
		t.Fatal("expected limiter state")
	}
	state.protectionCap = 3
	state.protectionAt = now
	if tryAcquireChannelRPMAt(groupID, cfg, now+1) {
		t.Fatal("expected the reduced post-429 limit to reject another request")
	}
	if !tryAcquireChannelRPMAt(groupID, cfg, now+time.Minute.Milliseconds()+1) {
		t.Fatal("expected the limit to recover after the ramp")
	}
	resetChannelRPMState(groupID)
}
