package graph

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	shadowpb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
)

type ClaimTokens interface {
	Put(ctx context.Context, token, namespace string, ttl time.Duration) error
	Take(ctx context.Context, token string) (string, error)
}

func (c *DevicesController) SetClaimTokens(s ClaimTokens) { c.claims = s }

func (c *DevicesController) BulkCreate(ctx context.Context, req *connect.Request[devpb.BulkCreateRequest]) (*connect.Response[devpb.BulkCreateResponse], error) {
	ns := req.Msg.GetNamespace()
	if ns == "" || len(req.Msg.GetDevices()) == 0 {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace and devices are required")
	}
	if err := c.allowCreate(ctx, ns, len(req.Msg.GetDevices())); err != nil {
		return nil, err
	}
	out := make([]*devpb.Device, 0, len(req.Msg.GetDevices()))
	for _, dev := range req.Msg.GetDevices() {
		created, err := c.createInNamespace(ctx, ns, dev)
		if err != nil {
			return nil, err
		}
		out = append(out, created)
	}
	return connect.NewResponse(&devpb.BulkCreateResponse{Devices: out}), nil
}

func (c *DevicesController) IssueClaim(ctx context.Context, req *connect.Request[devpb.ClaimIssueRequest]) (*connect.Response[devpb.ClaimIssueResponse], error) {
	ns := req.Msg.GetNamespace()
	if ns == "" || req.Msg.GetExpiresAt() <= time.Now().Unix() {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace and a future expires_at are required")
	}
	if c.claims == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "claim tokens are not configured")
	}
	if err := c.requireNamespace(ctx, ns, access.Level_ADMIN); err != nil {
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not mint a claim token")
	}
	token := hex.EncodeToString(buf)
	ttl := time.Until(time.Unix(req.Msg.GetExpiresAt(), 0))
	if err := c.claims.Put(ctx, token, ns, ttl); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not store the claim token")
	}
	return connect.NewResponse(&devpb.ClaimIssueResponse{Token: token, ExpiresAt: req.Msg.GetExpiresAt()}), nil
}

func (c *DevicesController) Claim(ctx context.Context, req *connect.Request[devpb.ClaimRequest]) (*connect.Response[devpb.ClaimResponse], error) {
	if c.claims == nil || req.Msg.GetToken() == "" || req.Msg.GetDevice() == nil {
		return nil, apierr.Code(connect.CodeInvalidArgument, "token and device are required")
	}
	ns, err := c.claims.Take(ctx, req.Msg.GetToken())
	if err != nil || ns == "" {
		return nil, apierr.Code(connect.CodePermissionDenied, "claim token is not valid")
	}
	if err := c.allowCreate(ctx, ns, 1); err != nil {
		return nil, err
	}
	created, err := c.createInNamespace(ctx, ns, req.Msg.GetDevice())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&devpb.ClaimResponse{Device: created}), nil
}

func (c *DevicesController) RotateCertificate(ctx context.Context, req *connect.Request[devpb.RotateCertificateRequest]) (*connect.Response[devpb.Device], error) {
	dev, err := c.deviceForAdmin(ctx, req.Msg.GetUuid())
	if err != nil {
		return nil, err
	}
	next := req.Msg.GetCertificate()
	if err := sha256Fingerprint(next); err != nil {
		return nil, apierr.Codef(connect.CodeInvalidArgument, "Can't generate fingerprint: %v", err)
	}
	if dev.Certificate == nil {
		dev.Certificate = next
	} else if !bytes.Equal(dev.Certificate.Fingerprint, next.Fingerprint) {
		next.RolloverFingerprint = append([]byte(nil), dev.Certificate.Fingerprint...)
		next.RevokedFingerprints = append([][]byte(nil), dev.Certificate.RevokedFingerprints...)
		dev.Certificate = next
	}
	if _, err := c.col.ReplaceDocument(ctx, dev.Uuid, dev); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device")
	}
	return connect.NewResponse(dev), nil
}

func (c *DevicesController) RevokeCertificate(ctx context.Context, req *connect.Request[devpb.RevokeCertificateRequest]) (*connect.Response[devpb.Device], error) {
	dev, err := c.deviceForAdmin(ctx, req.Msg.GetUuid())
	if err != nil {
		return nil, err
	}
	if dev.Certificate == nil {
		dev.Certificate = &devpb.Certificate{}
	}
	fp := req.Msg.GetFingerprint()
	if len(fp) == 0 {
		dev.Certificate.Revoked = true
	} else {
		dev.Certificate.RevokedFingerprints = append(dev.Certificate.RevokedFingerprints, append([]byte(nil), fp...))
		if bytes.Equal(dev.Certificate.Fingerprint, fp) {
			dev.Certificate.Fingerprint = nil
		}
		if bytes.Equal(dev.Certificate.RolloverFingerprint, fp) {
			dev.Certificate.RolloverFingerprint = nil
		}
	}
	if _, err := c.col.ReplaceDocument(ctx, dev.Uuid, dev); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device")
	}
	return connect.NewResponse(dev), nil
}

func certificateAccepts(cert *devpb.Certificate, fp []byte) bool {
	if cert == nil || cert.Revoked || len(fp) == 0 {
		return false
	}
	if !bytes.Equal(cert.Fingerprint, fp) && !bytes.Equal(cert.RolloverFingerprint, fp) {
		return false
	}
	for _, rev := range cert.RevokedFingerprints {
		if bytes.Equal(rev, fp) {
			return false
		}
	}
	return true
}

func diffTags(before, after []string) (added, removed []string) {
	have := map[string]bool{}
	for _, t := range before {
		have[t] = true
	}
	keep := map[string]bool{}
	for _, t := range after {
		keep[t] = true
		if !have[t] {
			added = append(added, t)
		}
	}
	for _, t := range before {
		if !keep[t] {
			removed = append(removed, t)
		}
	}
	return added, removed
}

func (c *DevicesController) requireNamespace(ctx context.Context, ns string, level access.Level) error {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if requestor == "" {
		return apierr.Code(connect.CodeUnauthenticated, "account is required")
	}
	ok, got := c.ica_repo.AccessLevel(ctx, NewBlankAccountDocument(requestor), NewBlankNamespaceDocument(ns))
	if !ok || got < level {
		return apierr.Codef(connect.CodePermissionDenied, "No Access to Namespace %s", ns)
	}
	return nil
}

func (c *DevicesController) allowCreate(ctx context.Context, ns string, n int) error {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if requestor != "" {
		if err := c.requireNamespace(ctx, ns, access.Level_ADMIN); err != nil {
			return err
		}
	}
	quota, count, err := c.namespaceUsage(ctx, ns)
	if err != nil {
		return apierr.Code(connect.CodeInternal, "could not read the namespace quota")
	}
	if quota.GetMaxDevices() > 0 && count+n > int(quota.GetMaxDevices()) {
		return apierr.Code(connect.CodeResourceExhausted, "namespace device quota is full")
	}
	return nil
}

const countDevicesQuery = `
FOR d, e IN 1..1 OUTBOUND @ns GRAPH @permissions
FILTER IS_SAME_COLLECTION(@devices, d)
FILTER e.expires_at == null OR e.expires_at == 0 OR e.expires_at > DATE_NOW() / 1000
COLLECT WITH COUNT INTO n
RETURN n`

func (c *DevicesController) namespaceUsage(ctx context.Context, ns string) (*nspb.Quota, int, error) {
	var doc nspb.Namespace
	col, err := c.db.Collection(ctx, schema.NAMESPACES_COL)
	if err != nil {
		return nil, 0, err
	}
	if _, err := col.ReadDocument(ctx, ns, &doc); err != nil {
		return nil, 0, err
	}
	cr, err := c.db.Query(ctx, countDevicesQuery, map[string]interface{}{
		"ns":          schema.NAMESPACES_COL + "/" + ns,
		"permissions": schema.PERMISSIONS_GRAPH.Name,
		"devices":     schema.DEVICES_COL,
	})
	if err != nil {
		return doc.GetQuota(), 0, err
	}
	defer cr.Close()
	var n int
	_, _ = cr.ReadDocument(ctx, &n)
	return doc.GetQuota(), n, nil
}

func (c *DevicesController) createInNamespace(ctx context.Context, nsID string, dev *devpb.Device) (*devpb.Device, error) {
	log := c.log.Named("Create")
	device := Device{Device: dev}
	if device.Device == nil {
		device.Device = &devpb.Device{}
	}
	if device.Certificate != nil {
		if err := sha256Fingerprint(device.Certificate); err != nil {
			return nil, apierr.Codef(connect.CodeInvalidArgument, "Can't generate fingerprint: %v", err)
		}
	}
	device.Token = ""
	meta, err := c.col.CreateDocument(ctx, device)
	if err != nil {
		log.Warn("Error creating Device", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while creating Device")
	}
	device.Uuid = meta.ID.Key()
	device.DocumentMeta = meta
	ns := NewBlankNamespaceDocument(nsID)
	if err := c.ica_repo.Link(ctx, log, c.ns2dev, ns, &device, access.Level_ADMIN, access.Role_OWNER); err != nil {
		_, _ = c.col.RemoveDocument(ctx, device.Uuid)
		return nil, apierr.Code(connect.CodeInternal, "error creating Permission")
	}
	c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, device.Device, nsID, "")
	return device.Device, nil
}

func (c *DevicesController) deviceForAdmin(ctx context.Context, id string) (*devpb.Device, error) {
	if id == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "device id is required")
	}
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	acc := *NewBlankAccountDocument(requestor)
	dev := *NewBlankDeviceDocument(id)
	if err := c.ica_repo.AccessLevelAndGet(ctx, c.log, &acc, &dev); err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "Device not found or not enough Access Rights")
	}
	if dev.Access.GetLevel() < access.Level_ADMIN {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}
	return dev.Device, nil
}

const namespaceOfDeviceQuery = `
FOR v IN 1..1 INBOUND @device GRAPH @permissions
FILTER IS_SAME_COLLECTION(@namespaces, v)
LIMIT 1
RETURN v`

func (c *DevicesController) attachNamespace(ctx context.Context, device *devpb.Device) {
	ns, err := c.namespaceOf(ctx, device.GetUuid())
	if err != nil || ns == nil {
		return
	}
	if device.Access == nil {
		device.Access = &access.Access{}
	}
	nsID := ns.GetUuid()
	device.Access.Namespace = &nsID
	device.Quota = ns.GetQuota()
}

func (c *DevicesController) MessageRates(ctx context.Context, device string) (float64, float64, string) {
	ns, err := c.namespaceOf(ctx, device)
	if err != nil || ns == nil || ns.GetQuota() == nil {
		return 0, 0, ""
	}
	return ns.GetQuota().GetDeviceMessagesPerSecond(), ns.GetQuota().GetNamespaceMessagesPerSecond(), ns.GetUuid()
}

func (c *DevicesController) namespaceOf(ctx context.Context, id string) (*nspb.Namespace, error) {
	cr, err := c.db.Query(ctx, namespaceOfDeviceQuery, map[string]interface{}{
		"device":      schema.DEVICES_COL + "/" + id,
		"permissions": schema.PERMISSIONS_GRAPH.Name,
		"namespaces":  schema.NAMESPACES_COL,
	})
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	var ns nspb.Namespace
	meta, err := cr.ReadDocument(ctx, &ns)
	if driver.IsNoMoreDocuments(err) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	ns.Uuid = meta.ID.Key()
	return &ns, nil
}
