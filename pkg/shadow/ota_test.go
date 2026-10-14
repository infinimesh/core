package shadow_test

import (
	"encoding/json"
	"testing"

	"github.com/go-redis/redis/v8"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func otaState(fields map[string]any) *pb.State {
	data, err := structpb.NewStruct(map[string]any{"ota": fields})
	if err != nil {
		panic(err)
	}
	return &pb.State{Data: data}
}

func TestValidateStateAcceptsPartialOTA(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	req := &pb.Shadow{Device: "dev-a", Desired: otaState(map[string]any{
		"version": "1.4.0", "url": "https://example/fw",
		"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"size":   12, "updated_at": "2026-10-13T12:00:00Z",
	})}
	f.mocks.rdb.EXPECT().Get(mock.Anything, "dev-a:desired").Return(redis.NewStringResult("", redis.Nil))
	f.mocks.ps.EXPECT().TryPub(req, "mqtt.outgoing", "mqtt.outgoing/dev-a").Return()
	f.mocks.ps.EXPECT().TryPub(mock.MatchedBy(func(sh *pb.Shadow) bool {
		return sh.GetEvent() == pb.ShadowEvent_OTA_TARGET_SET && sh.GetDevice() == "dev-a" && sh.GetEventId() != req.GetEventId()
	}), "mqtt.outgoing", "mqtt.outgoing/dev-a").Return()

	resp, err := f.service.Patch(f.data.ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, pb.ShadowEvent_STATE, resp.GetEvent())
	f.mocks.ps.AssertNumberOfCalls(t, "TryPub", 2)
}

func TestValidateStateRejectsUnknownOTAField(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	req := &pb.Shadow{Device: "dev-a", Reported: otaState(map[string]any{"progress": 10})}
	_, err := f.service.Patch(f.data.ctx, req)
	assert.EqualError(t, err, "invalid_argument: ota field progress is not supported")
	f.mocks.ps.AssertNotCalled(t, "TryPub", mock.Anything, mock.Anything)
}

func TestPatchSameOTAVersionPublishesOnce(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	prev := otaState(map[string]any{"version": "1.4.0"})
	raw, err := json.Marshal(prev)
	require.NoError(t, err)
	req := &pb.Shadow{Device: "dev-a", Desired: otaState(map[string]any{"version": "1.4.0"})}
	f.mocks.rdb.EXPECT().Get(mock.Anything, "dev-a:desired").Return(redis.NewStringResult(string(raw), nil))
	f.mocks.ps.EXPECT().TryPub(req, "mqtt.outgoing", "mqtt.outgoing/dev-a").Return()

	_, err = f.service.Patch(f.data.ctx, req)
	assert.NoError(t, err)
	f.mocks.ps.AssertNumberOfCalls(t, "TryPub", 1)
}

func TestPatchRequestedVersionPublishesOTARequested(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	prev := otaState(map[string]any{"version": "1.0.0"})
	raw, err := json.Marshal(prev)
	require.NoError(t, err)
	req := &pb.Shadow{Device: "dev-a", Desired: otaState(map[string]any{"requested_version": "1.4.0"})}
	f.mocks.rdb.EXPECT().Get(mock.Anything, "dev-a:desired").Return(redis.NewStringResult(string(raw), nil))
	f.mocks.ps.EXPECT().TryPub(req, "mqtt.outgoing", "mqtt.outgoing/dev-a").Return()
	f.mocks.ps.EXPECT().TryPub(mock.MatchedBy(func(sh *pb.Shadow) bool {
		return sh.GetEvent() == pb.ShadowEvent_OTA_REQUESTED && sh.GetDevice() == "dev-a"
	}), "mqtt.outgoing", "mqtt.outgoing/dev-a").Return()

	_, err = f.service.Patch(f.data.ctx, req)
	assert.NoError(t, err)
	f.mocks.ps.AssertNumberOfCalls(t, "TryPub", 2)
}

func TestPatchReportsOTAStatusChange(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	prev := otaState(map[string]any{"status": "idle"})
	raw, err := json.Marshal(prev)
	require.NoError(t, err)
	req := &pb.Shadow{Device: "dev-a", Reported: otaState(map[string]any{"status": "downloading"})}
	f.mocks.rdb.EXPECT().Get(mock.Anything, "dev-a:reported").Return(redis.NewStringResult(string(raw), nil))
	f.mocks.ps.EXPECT().TryPub(req, "mqtt.incoming", "mqtt.incoming/dev-a").Return()
	f.mocks.ps.EXPECT().TryPub(mock.MatchedBy(func(sh *pb.Shadow) bool {
		return sh.GetEvent() == pb.ShadowEvent_OTA_STATUS_CHANGED
	}), "mqtt.incoming", "mqtt.incoming/dev-a").Return()

	_, err = f.service.Patch(f.data.ctx, req)
	assert.NoError(t, err)
}

func TestPatchManyRecordsEachOTAError(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	resp, err := f.service.PatchMany(f.data.ctx, &pb.PatchManyRequest{
		Devices: []string{"dev-a", "dev-b"},
		Desired: otaState(map[string]any{"status": "installed"}),
	})
	require.NoError(t, err)
	require.Len(t, resp.GetResults(), 2)
	assert.Equal(t, int32(0), resp.GetUpdated())
	assert.Contains(t, resp.Results[0].GetError(), "ota status")
	assert.Contains(t, resp.Results[1].GetError(), "ota status")
	f.mocks.ps.AssertNotCalled(t, "TryPub", mock.Anything, mock.Anything)
}
