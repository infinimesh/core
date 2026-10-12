package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/quota"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/shadow"
	structpb "google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	MaxBody       = 16 << 10
	defaultPerMin = 60
)

type PatchFunc func(ctx context.Context, device string, reported *shadow.State) error

type DesiredFunc func(ctx context.Context, device string) (data []byte, version uint64, err error)

type Handler struct {
	Key     []byte
	Patch   PatchFunc
	Desired DesiredFunc
	Now     func() time.Time
	Limit   int
	Quota   func(ctx context.Context, device string) (devicePerSec, namespacePerSec float64, namespace string)

	mu      sync.Mutex
	hits    map[string][]time.Time
	waiters map[string][]chan struct{}
	rates   *quota.Limiter
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) Register(route func(string, http.HandlerFunc)) {
	route("/v1/ingest", h.Post)
	route("/v1/ingest/desired", h.DesiredState)
}

type deviceAuth struct {
	id    string
	post  bool
	level access.Level
}

func (h *Handler) device(r *http.Request) (deviceAuth, error) {
	header := r.Header.Get("Authorization")
	if len(header) < 8 || header[:7] != "Bearer " {
		return deviceAuth{}, apierr.Code(connect.CodeUnauthenticated, "missing bearer token")
	}
	parsed, err := jwt.Parse(header[7:], func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
		}
		return h.Key, nil
	})
	if err != nil || !parsed.Valid {
		return deviceAuth{}, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	raw, _ := claims[inf.INFINIMESH_DEVICES_CLAIM].(map[string]interface{})
	if len(raw) != 1 {
		return deviceAuth{}, apierr.New(connect.CodePermissionDenied, apierr.ReasonIngestDeviceCount, "ingest token must hold exactly one device")
	}
	var id string
	var level access.Level
	for k, v := range raw {
		id = k
		n, ok := v.(float64)
		if !ok {
			return deviceAuth{}, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
		}
		level = access.Level(n)
	}
	post, _ := claims[inf.INFINIMESH_POST_CLAIM].(bool)
	if !(post || level >= access.Level_MGMT) {
		return deviceAuth{}, apierr.New(connect.CodePermissionDenied, apierr.ReasonReportedWrite, "reported state requires write access")
	}
	return deviceAuth{id: id, post: post, level: level}, nil
}

func (h *Handler) allow(id string) bool {
	limit := h.Limit
	if limit <= 0 {
		limit = defaultPerMin
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits == nil {
		h.hits = map[string][]time.Time{}
	}
	now := h.now()
	cut := now.Add(-time.Minute)
	kept := h.hits[id][:0]
	for _, ts := range h.hits[id] {
		if ts.After(cut) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		h.hits[id] = kept
		return false
	}
	h.hits[id] = append(kept, now)
	return true
}

func (h *Handler) allowQuota(ctx context.Context, id string) bool {
	if h.Quota == nil {
		return true
	}
	perDevice, perNS, ns := h.Quota(ctx, id)
	h.mu.Lock()
	if h.rates == nil {
		h.rates = quota.New()
		h.rates.SetNow(h.now)
	}
	lim := h.rates
	h.mu.Unlock()
	return lim.Allow(id, perDevice) && lim.Allow("ns:"+ns, perNS)
}

func (h *Handler) Post(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	dev, err := h.device(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !h.allow(dev.id) || !h.allowQuota(r.Context(), dev.id) {
		writeErr(w, apierr.New(connect.CodeResourceExhausted, apierr.ReasonRateLimited, "rate limit exceeded"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		writeErr(w, apierr.Code(connect.CodeInvalidArgument, "failed to read body"))
		return
	}
	if len(body) > MaxBody {
		writeErr(w, apierr.New(connect.CodeInvalidArgument, apierr.ReasonBodyTooLarge, "body exceeds 16KB"))
		return
	}
	if !json.Valid(body) {
		writeErr(w, apierr.Code(connect.CodeInvalidArgument, "body must be JSON"))
		return
	}
	var data structpb.Struct
	if err := data.UnmarshalJSON(body); err != nil {
		writeErr(w, apierr.Code(connect.CodeInvalidArgument, "body must be a JSON object"))
		return
	}
	if h.Patch == nil {
		writeErr(w, apierr.Code(connect.CodeUnavailable, "ingest is not configured"))
		return
	}
	state := &shadow.State{Timestamp: timestamppb.New(h.now()), Data: &data}
	if err := h.Patch(r.Context(), dev.id, state); err != nil {
		writeErr(w, err)
		return
	}
	h.notify(dev.id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DesiredState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	dev, err := h.device(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	wait := 0
	if raw := r.URL.Query().Get("wait"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 60 {
			writeErr(w, apierr.Code(connect.CodeInvalidArgument, "wait must be 0..60 seconds"))
			return
		}
		wait = n
	}
	body, version, err := h.desired(r.Context(), dev.id)
	if err != nil {
		writeErr(w, err)
		return
	}
	tag := etag(version, body)
	if r.Header.Get("If-None-Match") == tag {
		if wait == 0 {
			w.Header().Set("ETag", tag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		defer timer.Stop()
		ch := h.wait(dev.id)
		select {
		case <-ch:
		case <-timer.C:
			w.Header().Set("ETag", tag)
			w.WriteHeader(http.StatusNotModified)
			return
		case <-r.Context().Done():
			return
		}
		body, version, err = h.desired(r.Context(), dev.id)
		if err != nil {
			writeErr(w, err)
			return
		}
		tag = etag(version, body)
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("X-Shadow-Version", strconv.FormatUint(version, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *Handler) desired(ctx context.Context, id string) ([]byte, uint64, error) {
	if h.Desired == nil {
		return []byte("{}"), 0, nil
	}
	return h.Desired(ctx, id)
}

func (h *Handler) notify(id string) {
	h.mu.Lock()
	waiters := h.waiters[id]
	delete(h.waiters, id)
	h.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (h *Handler) wait(id string) <-chan struct{} {
	ch := make(chan struct{})
	h.mu.Lock()
	if h.waiters == nil {
		h.waiters = map[string][]chan struct{}{}
	}
	h.waiters[id] = append(h.waiters[id], ch)
	h.mu.Unlock()
	return ch
}

func etag(version uint64, body []byte) string {
	sum := sha256.Sum256(append([]byte(strconv.FormatUint(version, 10)), body...))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated:
		code = http.StatusUnauthorized
	case connect.CodePermissionDenied:
		code = http.StatusForbidden
	case connect.CodeResourceExhausted:
		code = http.StatusTooManyRequests
	case connect.CodeUnavailable:
		code = http.StatusServiceUnavailable
	}
	http.Error(w, err.Error(), code)
}
