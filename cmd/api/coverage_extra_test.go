package main

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/infinimesh/core/pkg/graph"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/node/nodeconnect"
	"github.com/infinimesh/proto/shadow"
)

type apiShadow struct {
	shadow.ShadowServiceClient
	getRes    *shadow.GetResponse
	getErr    error
	removeRes *shadow.Shadow
	removeErr error
	stream    *scriptStream
	streamErr error
	streamReq *shadow.StreamShadowRequest
	patched   string
}

func (c *apiShadow) Get(_ context.Context, _ *shadow.GetRequest, _ ...grpc.CallOption) (*shadow.GetResponse, error) {
	return c.getRes, c.getErr
}

func (c *apiShadow) Remove(_ context.Context, _ *shadow.RemoveRequest, _ ...grpc.CallOption) (*shadow.Shadow, error) {
	return c.removeRes, c.removeErr
}

func (c *apiShadow) Patch(_ context.Context, in *shadow.Shadow, _ ...grpc.CallOption) (*shadow.Shadow, error) {
	c.patched = in.GetDevice()
	return in, nil
}

func (c *apiShadow) StreamShadow(_ context.Context, in *shadow.StreamShadowRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[shadow.Shadow], error) {
	c.streamReq = in
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	return c.stream, nil
}

type scriptStream struct {
	grpc.ClientStream
	msgs []*shadow.Shadow
	n    int
}

func (s *scriptStream) Recv() (*shadow.Shadow, error) {
	if s == nil || s.n >= len(s.msgs) {
		return nil, io.EOF
	}
	m := s.msgs[s.n]
	s.n++
	return m, nil
}

type fixedCtx struct{ ctx context.Context }

func (f fixedCtx) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(f.ctx, req)
	}
}

func (f fixedCtx) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (f fixedCtx) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(f.ctx, conn)
	}
}

func scopeCtx(devices map[string]access.Level) context.Context {
	ctx := context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, devices)
	return context.WithValue(ctx, inf.InfinimeshPostCtxKey, true)
}

func TestShadowAPIGetRemoveAndStream(t *testing.T) {
	client := &apiShadow{}
	api := NewShadowAPI(zap.NewNop(), client)
	api.SetRegistry(&fakeScopeDevices{}, &fakeScopeNamespaces{})

	if _, err := api.Get(context.Background(), connect.NewRequest(&shadow.GetRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("get %v", err)
	}
	ctx := scopeCtx(map[string]access.Level{"dev": access.Level_MGMT})
	client.getErr = errors.New("down")
	if _, err := api.Get(ctx, connect.NewRequest(&shadow.GetRequest{})); err == nil {
		t.Fatal("get error")
	}
	client.getErr = nil
	client.getRes = &shadow.GetResponse{Shadows: []*shadow.Shadow{{Device: "dev"}}}
	res, err := api.Get(ctx, connect.NewRequest(&shadow.GetRequest{}))
	if err != nil || res.Msg.GetShadows()[0].GetDevice() != "dev" {
		t.Fatalf("get ok %v", err)
	}

	if _, err := api.Remove(context.Background(), connect.NewRequest(&shadow.RemoveRequest{Device: "dev"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("remove auth %v", err)
	}
	if _, err := api.Remove(scopeCtx(map[string]access.Level{}), connect.NewRequest(&shadow.RemoveRequest{Device: "dev"})); err == nil {
		t.Fatal("remove scope")
	}
	client.removeErr = errors.New("down")
	if _, err := api.Remove(ctx, connect.NewRequest(&shadow.RemoveRequest{Device: "dev", StateKey: shadow.StateKey_DESIRED})); err == nil {
		t.Fatal("remove error")
	}
	client.removeErr = nil
	client.removeRes = &shadow.Shadow{Device: "dev"}
	gone, err := api.Remove(ctx, connect.NewRequest(&shadow.RemoveRequest{Device: "dev"}))
	if err != nil || gone.Msg.GetDevice() != "dev" {
		t.Fatalf("remove ok %v", err)
	}

	client.streamErr = errors.New("nostream")
	if err := api.StreamShadow(ctx, connect.NewRequest(&shadow.StreamShadowRequest{}), nil); err == nil {
		t.Fatal("stream start")
	}
	client.streamErr = nil
	client.stream = &scriptStream{msgs: []*shadow.Shadow{{Device: "dev"}}}
	pull := func(sync bool) {
		t.Helper()
		path, handler := nodeconnect.NewShadowServiceHandler(api, connect.WithInterceptors(fixedCtx{ctx: ctx}))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		httpClient := nodeconnect.NewShadowServiceClient(srv.Client(), srv.URL)
		var stream *connect.ServerStreamForClient[shadow.Shadow]
		var err error
		req := connect.NewRequest(&shadow.StreamShadowRequest{Namespace: "plant"})
		if sync {
			stream, err = httpClient.StreamShadowSync(context.Background(), req)
		} else {
			stream, err = httpClient.StreamShadow(context.Background(), req)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !stream.Receive() {
			t.Fatal(stream.Err())
		}
		if stream.Msg().GetDevice() != "dev" {
			t.Fatalf("recv %+v", stream.Msg())
		}
		_ = stream.Receive()
	}
	pull(false)
	client.stream = &scriptStream{msgs: []*shadow.Shadow{{Device: "dev"}}}
	pull(true)
	if client.streamReq == nil || !client.streamReq.GetSync() || len(client.streamReq.GetDevices()) != 1 {
		t.Fatalf("sync req %+v", client.streamReq)
	}
}

func devNS(id, ns string, tags ...string) *devpb.Device {
	return &devpb.Device{
		Uuid: id, Title: id, Tags: tags,
		Access: &access.Access{Level: access.Level_MGMT, Namespace: &ns},
	}
}

func otaOn(id, provider string) *nspb.Namespace {
	return &nspb.Namespace{Uuid: id, Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: provider}}
}

func TestOTAEdges(t *testing.T) {
	var nilAPI *ShadowAPI
	nilAPI.SetPlugins(&fakeOTAPlugins{})
	api := &ShadowAPI{log: zap.NewNop()}
	api.SetAuditor(&recordAudit{})
	api.SetPlugins(nil)

	if reportedOTAOnly(nil) || reportedOTAOnly(&shadow.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{}), "x": structpb.NewStringValue("y"),
	}}}) {
		t.Fatal("reported only")
	}
	only := &shadow.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{}),
	}}}
	if !reportedOTAOnly(only) {
		t.Fatal("ota only")
	}
	if addonMayPatch(nil) || addonMayPatch(&shadow.Shadow{Connection: &shadow.ConnectionState{}}) || addonMayPatch(&shadow.Shadow{}) {
		t.Fatal("addon may")
	}
	if err := addonPatchError(&shadow.Shadow{Reported: &shadow.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"temp": structpb.NewNumberValue(1),
	}}}}); err == nil {
		t.Fatal("reported mix")
	}
	if err := addonPatchError(&shadow.Shadow{}); err == nil {
		t.Fatal("addon empty")
	}
	if addonPatchError(&shadow.Shadow{Reported: only}) != nil {
		t.Fatal("addon reported")
	}
	if desiredRequestsVersion(&shadow.State{Data: &structpb.Struct{}}) || !desiredRequestsVersion(otaStateFields(map[string]any{"ota": map[string]any{"requested_version": "1"}})) {
		t.Fatal("requested")
	}

	ns := otaOn("plant", "none")
	cache := map[string]*nspb.Namespace{"plant": ns}
	got, err := api.namespaceDoc(context.Background(), "plant", cache)
	if err != nil || got.GetUuid() != "plant" {
		t.Fatalf("cache %v", err)
	}
	if _, err := api.namespaceDoc(context.Background(), "plant", map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("no registry")
	}
	api.namespaces = &fakeScopeNamespaces{}
	if _, err := api.namespaceDoc(context.Background(), "", map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("empty ns")
	}
	api.namespaces = &fakeScopeNamespaces{getErr: errors.New("down")}
	if _, err := api.namespaceDoc(context.Background(), "plant", map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("config")
	}
	api.namespaces = &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{"plant": ns, "off": {Uuid: "off"}}}

	dev := devNS("dev", "plant")
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	if err := api.allowDeviceOTA(ctx, dev, map[string]*nspb.Namespace{}); err != nil {
		t.Fatal(err)
	}
	plugCtx := context.WithValue(ctx, inf.InfinimeshPluginCtxKey, "plug")
	if err := api.allowDeviceOTA(plugCtx, dev, map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("none rejects plugin")
	}
	hawk := otaOn("hawk", "hawkbit")
	api.namespaces.(*fakeScopeNamespaces).byID["hawk"] = hawk
	if err := api.allowDeviceOTA(ctx, devNS("dev", "hawk"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("needs addon")
	}
	if err := api.allowDeviceOTA(plugCtx, devNS("dev", "hawk"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("plugins nil")
	}
	api.plugins = &fakeOTAPlugins{ok: false}
	claim := context.WithValue(plugCtx, inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	claim = context.WithValue(claim, inf.InfinimeshJTICtxKey, "jti")
	if err := api.allowDeviceOTA(claim, devNS("dev", "hawk"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("plugin miss")
	}
	api.plugins = &fakeOTAPlugins{ok: true, plug: graphPlugin("plug", "jti", "hawkbit", "other", false)}
	if err := api.allowDeviceOTA(claim, devNS("dev", "hawk"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("private ns")
	}
	api.plugins = &fakeOTAPlugins{ok: true, plug: graphPlugin("plug", "jti", "hawkbit", "hawk", true)}
	if err := api.allowDeviceOTA(claim, devNS("dev", "hawk"), map[string]*nspb.Namespace{}); err != nil {
		t.Fatal(err)
	}
	if err := api.allowDeviceOTA(ctx, devNS("dev", "off"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("inactive")
	}

	if _, err := api.authorizeDeviceTokenOTA(ctx, "dev"); err == nil {
		t.Fatal("no devices")
	}
	api.devices = &fakeScopeDevices{devices: []*devpb.Device{devNS("dev", "plant")}}
	if nsID, err := api.authorizeDeviceTokenOTA(ctx, "dev"); err != nil || nsID != "plant" {
		t.Fatalf("auth device %s %v", nsID, err)
	}
	if _, err := api.authorizeDeviceTokenOTA(ctx, "missing"); err == nil {
		t.Fatal("missing device")
	}

	if _, _, err := api.managedDevice(context.Background(), "dev"); err == nil {
		t.Fatal("no caller")
	}
	if _, _, err := api.managedDevice(plugCtx, "dev"); err == nil {
		t.Fatal("plugin managed")
	}
	if _, _, err := api.managedDevice(ctx, "nope"); err == nil {
		t.Fatal("outside")
	}
	if dev, nsID, err := api.managedDevice(ctx, "dev"); err != nil || dev.GetUuid() != "dev" || nsID != "plant" {
		t.Fatalf("managed %v", err)
	}

	if connectMessage(nil) != "" || connectMessage(errors.New("plain")) != "plain" {
		t.Fatal("message")
	}
	if _, _, err := api.selectOTA(context.Background(), "", "", nil); err == nil {
		t.Fatal("select no caller")
	}
	allowed, denied, err := api.selectOTA(ctx, "", "", []string{"missing", "dev"})
	if err != nil || len(allowed) != 1 || len(denied) != 1 {
		t.Fatalf("select %+v %+v %v", allowed, denied, err)
	}
	allowed, denied, err = api.selectOTA(claim, "hawk", "", nil)
	if err != nil || len(allowed) != 0 {
		t.Fatalf("hawk select %+v %v", denied, err)
	}

	if _, err := api.devicesForOTA(context.Background(), "", ""); err == nil {
		t.Fatal("devices no caller")
	}
	saved := api.devices
	api.devices = nil
	if _, err := api.devicesForOTA(ctx, "", ""); err == nil {
		t.Fatal("devices nil")
	}
	api.devices = saved
	api.plugins = nil
	if _, err := api.devicesForOTA(plugCtx, "", ""); err == nil {
		t.Fatal("plugin registry")
	}
	api.plugins = &fakeOTAPlugins{ok: false}
	if _, err := api.devicesForOTA(claim, "", ""); err == nil {
		t.Fatal("bad plugin")
	}
	api.plugins = &fakeOTAPlugins{ok: true, plug: graphPlugin("plug", "jti", "hawkbit", "", false)}
	if devs, err := api.devicesForOTA(claim, "hawk", ""); err != nil || devs != nil {
		t.Fatalf("empty private %v", err)
	}
	api.plugins = &fakeOTAPlugins{ok: true, plug: graphPlugin("plug", "jti", "hawkbit", "hawk", false)}
	if devs, err := api.devicesForOTA(claim, "plant", ""); err != nil || devs != nil {
		t.Fatalf("mismatch %v", err)
	}
	if _, err := api.devicesForOTA(claim, "hawk", ""); err != nil {
		t.Fatal(err)
	}
	api.plugins = &fakeOTAPlugins{ok: true, plug: graphPlugin("plug", "jti", "hawkbit", "hawk", true)}
	if _, err := api.devicesForOTA(claim, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := api.allowAccountOTARequest(plugCtx, dev, map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("account request plugin")
	}
	if err := api.allowAccountOTARequest(ctx, devNS("dev", "off"), map[string]*nspb.Namespace{}); err == nil {
		t.Fatal("account inactive")
	}
	if err := api.allowAccountOTARequest(ctx, dev, map[string]*nspb.Namespace{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.selectAccountOTA(context.Background(), "", "", nil); err == nil {
		t.Fatal("select account none")
	}
	if _, _, err := api.selectAccountOTA(claim, "", "", nil); err == nil {
		t.Fatal("select account plugin")
	}
	allowed, denied, err = api.selectAccountOTA(ctx, "", "", []string{"missing"})
	if err != nil || len(denied) != 1 || len(allowed) != 0 {
		t.Fatalf("denied account %+v %v", denied, err)
	}
	if _, _, err := api.selectAccountOTA(ctx, "plant", "", nil); err != nil {
		t.Fatal(err)
	}

	resp, err := api.storeDesiredOTA(ctx, nil, []*shadow.DevicePatchResult{{Device: "x", Error: "no"}}, otaDesired("1", time.Now()), auditOTAAssign)
	if err != nil || resp.GetUpdated() != 0 {
		t.Fatalf("empty store %v", err)
	}
	api.client = &stubShadowClient{err: errors.New("down")}
	if _, err := api.storeDesiredOTA(ctx, []*devpb.Device{dev}, nil, otaDesired("1", time.Now()), auditOTAAssign); err == nil {
		t.Fatal("store error")
	}
	api.client = &stubShadowClient{res: &shadow.PatchManyResponse{Updated: 1, Results: []*shadow.DevicePatchResult{
		{Device: "dev"}, {Device: "dev", Error: "skip"}, {Device: ""},
	}}}
	api.auditor = nil
	if _, err := api.storeDesiredOTA(ctx, []*devpb.Device{dev}, nil, otaDesired("1", time.Now()), auditOTAAssign); err != nil {
		t.Fatal(err)
	}
	audit := &recordAudit{}
	api.auditor = audit
	if _, err := api.storeDesiredOTA(ctx, []*devpb.Device{dev}, nil, otaDesired("1", time.Now()), auditOTARequest); err != nil || len(audit.rows) != 1 {
		t.Fatalf("audit %+v %v", audit.rows, err)
	}
	if st := otaDocument(map[string]any{"bad": make(chan int)}); st.GetData() != nil {
		t.Fatal("bad document")
	}

	if _, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{})); err == nil {
		t.Fatal("assign version")
	}
	if _, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Version: "1"})); err == nil {
		t.Fatal("assign target")
	}
	if _, err := api.RequestOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{})); err == nil {
		t.Fatal("request version")
	}
	if _, err := api.RequestOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Version: "1"})); err == nil {
		t.Fatal("request target")
	}
	api.client = &stubShadowClient{}
	assigned, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Version: "1", Devices: []string{"dev"}}))
	if err != nil || assigned.Msg.GetUpdated() != 1 {
		t.Fatalf("assign %v", err)
	}
	requested, err := api.RequestOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Version: "2", Devices: []string{"dev"}}))
	if err != nil || requested.Msg.GetUpdated() != 1 {
		t.Fatalf("request %v", err)
	}
}

func graphPlugin(id, token, provider, ns string, public bool) graph.OTAPlugin {
	return graph.OTAPlugin{ID: id, TokenID: token, Provider: provider, Namespace: ns, Public: public}
}

func TestPatchManyWithoutOTA(t *testing.T) {
	api := &ShadowAPI{log: zap.NewNop(), client: &stubShadowClient{}}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	if _, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{})); err == nil {
		t.Fatal("desired")
	}
	desired := &shadow.State{}
	plug := context.WithValue(ctx, inf.InfinimeshPluginCtxKey, "plug")
	if _, err := api.PatchMany(plug, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired})); err == nil {
		t.Fatal("plugin")
	}
	if _, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired})); err == nil {
		t.Fatal("registry")
	}
	ns := "plant"
	api.devices = &fakeScopeDevices{err: errors.New("down")}
	if _, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired})); err == nil {
		t.Fatal("list")
	}
	api.devices = &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "low", Tags: []string{"keep"}, Access: &access.Access{Level: access.Level_READ, Namespace: &ns}},
		{Uuid: "west", Tags: []string{"keep"}, Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("west")}},
		{Uuid: "dev", Tags: []string{"keep"}, Access: &access.Access{Level: access.Level_MGMT, Namespace: &ns}},
	}}
	empty, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired, Namespace: "plant", Tag: "missing"}))
	if err != nil || empty.Msg.GetUpdated() != 0 {
		t.Fatalf("empty %v", err)
	}
	api.client = &stubShadowClient{err: errors.New("down")}
	if _, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired, Devices: []string{"dev"}})); err == nil {
		t.Fatal("patch")
	}
	api.client = &stubShadowClient{}
	res, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{Desired: desired, Namespace: "plant", Tag: "keep"}))
	if err != nil || res.Msg.GetUpdated() != 1 {
		t.Fatalf("patch many %v %+v", err, res)
	}
}

func TestPluginTokenEdges(t *testing.T) {
	ctx := context.Background()
	if err := publishPluginToken(ctx, nil, "id"); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := publishPluginToken(ctx, rdb, ""); err != nil {
		t.Fatal(err)
	}
	if err := listenPluginTokens(ctx, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := listenPluginTokens(ctx, rdb, nil); err != nil {
		t.Fatal(err)
	}
	dead, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 30 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { _ = down.Close() })
	if err := listenPluginTokens(dead, down, func(string) {}); err == nil {
		t.Fatal("listen down")
	}

	var mu sync.Mutex
	var got []string
	live, stop := context.WithCancel(context.Background())
	defer stop()
	if err := listenPluginTokens(live, rdb, func(id string) {
		mu.Lock()
		got = append(got, id)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Publish(ctx, pluginTokenChannel, "").Err(); err != nil {
		t.Fatal(err)
	}
	if err := publishPluginToken(ctx, rdb, "dev"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "dev" {
		t.Fatalf("payloads %v", got)
	}
}

type ingestShadow struct {
	shadow.ShadowServiceClient
	res *shadow.GetResponse
	err error
}

func (c *ingestShadow) Patch(_ context.Context, in *shadow.Shadow, _ ...grpc.CallOption) (*shadow.Shadow, error) {
	return in, nil
}

func (c *ingestShadow) Get(_ context.Context, _ *shadow.GetRequest, _ ...grpc.CallOption) (*shadow.GetResponse, error) {
	return c.res, c.err
}

func TestMountIngest(t *testing.T) {
	key := []byte("ingest-secret")
	client := &ingestShadow{}
	router := mux.NewRouter()
	mountIngest(router, client, key, func(context.Context, string) (float64, float64, string) {
		return 1000, 1000, "plant"
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		inf.INFINIMESH_DEVICES_CLAIM: map[string]any{"dev": float64(access.Level_MGMT)},
		inf.INFINIMESH_POST_CLAIM:    true,
	})
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call(http.MethodPost, "/v1/ingest", `{"a":1}`)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("post %d", res.StatusCode)
	}

	client.err = errors.New("down")
	res = call(http.MethodGet, "/v1/ingest/desired", "")
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("get error")
	}
	client.err = nil
	client.res = &shadow.GetResponse{Shadows: []*shadow.Shadow{{Device: "other", Desired: &shadow.State{}}}}
	res = call(http.MethodGet, "/v1/ingest/desired", "")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "{}" {
		t.Fatalf("mismatch %s", body)
	}
	data, err := structpb.NewStruct(map[string]any{"led": true})
	if err != nil {
		t.Fatal(err)
	}
	client.res = &shadow.GetResponse{Shadows: []*shadow.Shadow{{
		Device:  "dev",
		Desired: &shadow.State{Version: 3, Data: data},
	}}}
	res = call(http.MethodGet, "/v1/ingest/desired", "")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "led") {
		t.Fatalf("desired %d %s", res.StatusCode, body)
	}
	client.res = &shadow.GetResponse{Shadows: []*shadow.Shadow{{
		Device: "dev",
		Desired: &shadow.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
			"n": structpb.NewNumberValue(math.NaN()),
		}}},
	}}}
	res = call(http.MethodGet, "/v1/ingest/desired", "")
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("nan desired")
	}
	client.res = &shadow.GetResponse{}
	res = call(http.MethodGet, "/v1/ingest/desired", "")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "{}" {
		t.Fatalf("empty %s", body)
	}
}
