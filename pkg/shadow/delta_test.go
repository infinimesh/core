package shadow_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	pubsub_mocks "github.com/infinimesh/core/mocks/github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func stateData(t *testing.T, fields map[string]any) *pb.State {
	t.Helper()
	data, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.State{Data: data}
}

func TestStreamShadow_MergesStoredState(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	f.mocks.srv.EXPECT().Context().Return(f.data.ctx)

	mget := redis.NewSliceCmd(f.data.ctx)
	mget.SetVal([]interface{}{
		`{"data":{"a":1}}`,
		`{"data":{"keep":1}}`,
		`{"connected":false}`,
	})
	f.mocks.rdb.EXPECT().MGet(
		f.data.ctx,
		shadow.Key("dev", pb.StateKey_REPORTED),
		shadow.Key("dev", pb.StateKey_DESIRED),
		shadow.Key("dev", pb.StateKey_CONNECTION),
	).Return(mget)

	var got *pb.Shadow
	f.mocks.srv.EXPECT().Send(mock.MatchedBy(func(sh *pb.Shadow) bool {
		got = proto.Clone(sh).(*pb.Shadow)
		return true
	})).Return(assert.AnError)
	f.mocks.ps.EXPECT().AddSub(mock.MatchedBy(func(ch chan interface{}) bool {
		ch <- &pb.Shadow{
			Device:     "dev",
			Reported:   stateData(t, map[string]any{"b": 2.0}),
			Connection: &pb.ConnectionState{Connected: true},
		}
		return true
	}), "mqtt.incoming/dev", "mqtt.outgoing/dev").Return()
	f.mocks.ps.EXPECT().Unsub(mock.MatchedBy(func(ch chan interface{}) bool {
		close(ch)
		return true
	})).Return()

	err := f.service.StreamShadow(&pb.StreamShadowRequest{Devices: []string{"dev"}}, f.mocks.srv)
	assert.NoError(t, err)
	assert.Equal(t, 1.0, got.GetReported().GetData().GetFields()["a"].GetNumberValue())
	assert.Equal(t, 2.0, got.GetReported().GetData().GetFields()["b"].GetNumberValue())
	assert.Equal(t, 1.0, got.GetDesired().GetData().GetFields()["keep"].GetNumberValue())
	assert.True(t, got.GetConnection().GetConnected())
}

func TestStreamShadow_OnlyDeltaSkipsStoredState(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	f.mocks.srv.EXPECT().Context().Return(f.data.ctx)

	var got *pb.Shadow
	f.mocks.srv.EXPECT().Send(mock.MatchedBy(func(sh *pb.Shadow) bool {
		got = proto.Clone(sh).(*pb.Shadow)
		return true
	})).Return(assert.AnError)
	f.mocks.ps.EXPECT().AddSub(mock.MatchedBy(func(ch chan interface{}) bool {
		ch <- &pb.Shadow{Device: "dev", Reported: stateData(t, map[string]any{"b": 2.0})}
		return true
	}), "mqtt.incoming/dev", "mqtt.outgoing/dev").Return()
	f.mocks.ps.EXPECT().Unsub(mock.MatchedBy(func(ch chan interface{}) bool {
		close(ch)
		return true
	})).Return()

	err := f.service.StreamShadow(&pb.StreamShadowRequest{
		Devices:   []string{"dev"},
		OnlyDelta: true,
	}, f.mocks.srv)
	assert.NoError(t, err)
	assert.Nil(t, got.GetReported().GetData().GetFields()["a"])
	assert.Equal(t, 2.0, got.GetReported().GetData().GetFields()["b"].GetNumberValue())
	f.mocks.rdb.AssertNotCalled(t, "MGet", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestStreamShadow_IncludeFiltersState(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	st := &recStream{
		ctx:  context.Background(),
		ch:   make(chan *pb.Shadow, 8),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Namespace: "plant",
			Account:   "acc",
			OnlyDelta: true,
			Include:   []pb.StateKey{pb.StateKey_CONNECTION},
		}, st)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var added *pb.Shadow
	for added == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{
			Device: "dev-a", Namespace: "plant", Title: "Pump", Event: pb.ShadowEvent_DEVICE_ADDED,
		}, "devices.lifecycle")
		select {
		case sh := <-st.ch:
			if sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED && sh.GetDevice() == "dev-a" {
				added = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if added == nil || added.GetTitle() != "Pump" {
		t.Fatalf("membership %+v", added)
	}

	deadline = time.Now().Add(2 * time.Second)
	var state *pb.Shadow
	for state == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{
			Device:     "dev-a",
			Reported:   stateData(t, map[string]any{"a": 1.0}),
			Connection: &pb.ConnectionState{Connected: true},
		}, "mqtt.incoming/dev-a")
		select {
		case sh := <-st.ch:
			if sh.GetEvent() == pb.ShadowEvent_STATE && sh.GetDevice() == "dev-a" {
				state = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if state.GetReported() != nil || !state.GetConnection().GetConnected() {
		t.Fatalf("filtered %+v", state)
	}

	close(st.stop)
	ps.TryPub(&pb.Shadow{Device: "dev-a"}, "mqtt.incoming/dev-a")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}

func TestStreamShadow_UnknownIncludeKeepsState(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	f.mocks.ps = pubsub_mocks.NewMockPubSub(t)
	f.service = shadow.NewShadowServiceServer(f.mocks.log, f.mocks.rdb, f.mocks.ps)
	f.mocks.srv.EXPECT().Context().Return(f.data.ctx)

	var got *pb.Shadow
	f.mocks.srv.EXPECT().Send(mock.MatchedBy(func(sh *pb.Shadow) bool {
		got = proto.Clone(sh).(*pb.Shadow)
		return true
	})).Return(assert.AnError)
	f.mocks.ps.EXPECT().AddSub(mock.MatchedBy(func(ch chan interface{}) bool {
		ch <- &pb.Shadow{
			Device:     "dev",
			Reported:   stateData(t, map[string]any{"a": 1.0}),
			Desired:    stateData(t, map[string]any{"b": 2.0}),
			Connection: &pb.ConnectionState{Connected: true},
		}
		return true
	}), "mqtt.incoming/dev", "mqtt.outgoing/dev").Return()
	f.mocks.ps.EXPECT().Unsub(mock.MatchedBy(func(ch chan interface{}) bool {
		close(ch)
		return true
	})).Return()

	err := f.service.StreamShadow(&pb.StreamShadowRequest{
		Devices:   []string{"dev"},
		OnlyDelta: true,
		Include:   []pb.StateKey{pb.StateKey(99)},
	}, f.mocks.srv)
	assert.NoError(t, err)
	assert.NotNil(t, got.GetReported())
	assert.NotNil(t, got.GetDesired())
	assert.True(t, got.GetConnection().GetConnected())
}
