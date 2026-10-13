/*
Copyright © 2021-2023 Infinite Devices GmbH, Nikita Ivanovski info@slnt-opp.xyz

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/infinimesh/core/pkg/apierr"
	"time"

	"github.com/google/uuid"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	access "github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	"github.com/infinimesh/proto/pairing"
	shadowpb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Device struct {
	*devpb.Device
	driver.DocumentMeta
}

func (o *Device) ID() driver.DocumentID {
	return o.DocumentMeta.ID
}

func (o *Device) SetAccessLevel(level access.Level) {
	if o.Access == nil {
		o.Access = &access.Access{
			Level: level,
		}
		return
	}
	o.Access.Level = level
}

func (o *Device) GetAccess() *access.Access {
	if o.Access == nil {
		return &access.Access{
			Level: access.Level_NONE,
		}
	}
	return o.Access
}

func NewBlankDeviceDocument(key string) *Device {
	return &Device{
		Device: &devpb.Device{
			Uuid: key,
		},
		DocumentMeta: NewBlankDocument(schema.DEVICES_COL, key),
	}
}

func NewDeviceFromPB(dev *devpb.Device) (res *Device) {
	return &Device{
		Device:       dev,
		DocumentMeta: NewBlankDocument(schema.DEVICES_COL, dev.Uuid),
	}
}

type DevicesController struct {
	InfinimeshBaseController

	col driver.Collection // Devices Collection
	hfc pairing.PairingServiceClient

	ns2dev  driver.Collection // Namespaces to Devices permissions edge collection
	acc2dev driver.Collection // Accounts to Devices permissions edge collection

	ica_repo InfinimeshCommonActionsRepo // Infinimesh Common Actions Repository

	SIGNING_KEY []byte

	scope    ScopePublisher
	denylist TokenDenylist
	claims   ClaimTokens
	audit    DeviceAudit
}

type DeviceAudit interface {
	Record(ctx context.Context, action, device, account, tokenID, namespace string)
}

type TokenDenylist interface {
	Deny(ctx context.Context, id string, ttl time.Duration) error
}

func (c *DevicesController) SetDenylist(d TokenDenylist) {
	c.denylist = d
}

type ScopePublisher interface {
	PublishDeviceScope(ctx context.Context, in *shadowpb.Shadow, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

func (c *DevicesController) SetScopePublisher(p ScopePublisher) {
	c.scope = p
}

func (c *DevicesController) SetAuditor(a DeviceAudit) {
	c.audit = a
}

func (c *DevicesController) auditDevice(ctx context.Context, action, device, account, tokenID string) {
	if c == nil || c.audit == nil {
		return
	}
	c.audit.Record(ctx, action, device, account, tokenID, "")
}

func NewDevicesController(
	log *zap.Logger, db driver.Database,
	hfc pairing.PairingServiceClient,
	ica InfinimeshCommonActionsRepo,
) *DevicesController {
	ctx := context.TODO()
	col, _ := db.Collection(ctx, schema.DEVICES_COL)

	return &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: log.Named("DevicesController"), db: db,
		},
		col: col, hfc: hfc,
		ns2dev:  ica.GetEdgeCol(ctx, schema.NS2DEV),
		acc2dev: ica.GetEdgeCol(ctx, schema.ACC2DEV),

		ica_repo: ica,

		SIGNING_KEY: []byte("just-an-init-thing-replace-me"),
	}
}

func sha256Fingerprint(cert *devpb.Certificate) (err error) {
	if cert == nil {
		return errors.New("certificate is nil")
	}
	block, _ := pem.Decode([]byte(cert.PemData))
	if block == nil {
		return errors.New("coudn't decode PEM data")
	}

	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}

	s := sha256.New()
	_, err = s.Write(parsed.Raw)
	if err != nil {
		return err
	}

	cert.Algorithm = "sha256"
	cert.Fingerprint = s.Sum(nil)

	return nil
}

func (c *DevicesController) Create(ctx context.Context, _req *connect.Request[devpb.CreateRequest]) (*connect.Response[devpb.CreateResponse], error) {
	log := c.log.Named("Create")
	req := _req.Msg
	log.Debug("Create request received", zap.Any("request", req), zap.Any("context", ctx))

	var cached devpb.CreateResponse
	if loadIdem(_req.Header(), "devices.create", &cached) {
		return connect.NewResponse(&cached), nil
	}

	if req.Pairing != nil {
		resp, err := c._PairingCreate(ctx, req)
		if err == nil && resp != nil {
			saveIdem(_req.Header(), "devices.create", resp.Msg)
		}
		return resp, err
	}

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	ns_id := req.GetNamespace()
	if ns_id == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Namespace ID is required")
	}

	ns := NewBlankNamespaceDocument(ns_id)

	ok, level := c.ica_repo.AccessLevel(ctx, NewBlankAccountDocument(requestor), ns)
	if !ok || level < access.Level_ADMIN {
		return nil, apierr.Codef(connect.CodePermissionDenied, "No Access to Namespace %s", ns_id)
	}

	device := Device{Device: req.GetDevice()}

	if device.Device.Certificate != nil {
		err := sha256Fingerprint(device.Device.Certificate)
		if err != nil {
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

	err = c.ica_repo.Link(ctx, log, c.ns2dev, ns, &device, access.Level_ADMIN, access.Role_OWNER)
	if err != nil {
		log.Warn("Error creating edge", zap.Error(err))
		c.col.RemoveDocument(ctx, device.Uuid)
		return nil, apierr.Code(connect.CodeInternal, "error creating Permission")
	}

	c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, device.Device, ns_id, "")
	c.auditDevice(ctx, auditCreate, device.Uuid, requestor, "")

	resp := connect.NewResponse(&devpb.CreateResponse{
		Device: device.Device,
	})
	saveIdem(_req.Header(), "devices.create", resp.Msg)
	return resp, nil
}

func (c *DevicesController) _PairingCreate(ctx context.Context, req *devpb.CreateRequest) (*connect.Response[devpb.CreateResponse], error) {
	log := c.log.Named("PairingCreate")
	log.Debug("Request received")

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	ns_id := req.GetNamespace()
	if ns_id == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Namespace ID is required")
	}

	ns := NewBlankNamespaceDocument(ns_id)

	ok, level := c.ica_repo.AccessLevel(ctx, NewBlankAccountDocument(requestor), ns)
	if !ok || level < access.Level_ADMIN {
		return nil, apierr.Codef(connect.CodePermissionDenied, "No Access to Namespace %s", ns_id)
	}

	dev := req.GetDevice()
	device := Device{Device: dev}
	device.Token = ""

	meta, err := c.col.CreateDocument(ctx, device)
	if err != nil {
		log.Warn("Error creating Device", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while creating Device")
	}
	device.Uuid = meta.ID.Key()
	device.DocumentMeta = meta

	err = c.ica_repo.Link(ctx, log, c.ns2dev, ns, &device, access.Level_ADMIN, access.Role_OWNER)
	if err != nil {
		log.Warn("Error creating edge", zap.Error(err))
		c.col.RemoveDocument(ctx, device.Uuid)
		return nil, apierr.Code(connect.CodeInternal, "error creating Permission")
	}

	cleanup := func(err error) (*connect.Response[devpb.CreateResponse], error) {
		if _, d_err := c.delete(ctx, connect.NewRequest(device.Device), false); d_err != nil {
			log.Warn("Couldn't delete Device", zap.Error(d_err))
			return connect.NewResponse(&devpb.CreateResponse{
					Device: device.Device,
				}), connect.NewError(connect.CodeDataLoss, fmt.Errorf(
					"Couldn't delete freshly created device as well as set the certificate: %v", err,
				))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	cp, err := c.hfc.Send(ctx, &pairing.ControlPacket{
		Payload: append([]string{req.GetPairing().GetCode(), device.Uuid}, req.GetPairing().GetPayload()...),
	})
	if err != nil {
		log.Warn("Couldn't obtain certificate from Pairing", zap.Error(err))
		return cleanup(err)
	}

	if len(cp.GetPayload()) == 0 {
		log.Warn("Pairing connection Payload is empty")
		return cleanup(fmt.Errorf("issue with Pairing Payload: is empty"))
	}

	dev.Certificate = &devpb.Certificate{
		PemData: cp.GetPayload()[0],
	}
	if cp.GetAppId() != "" {
		dev.Tags = append(dev.Tags, cp.GetAppId())
	}

	err = sha256Fingerprint(dev.Certificate)
	if err != nil {
		log.Warn("Couldn't generate certificate Hash", zap.Error(err))
		return cleanup(err)
	}

	_, err = c.col.ReplaceDocument(ctx, device.Uuid, dev)
	if err != nil {
		log.Warn("Couldn't set Device Certificate", zap.Error(err))
		return cleanup(err)
	}

	c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, dev, ns_id, "")
	c.auditDevice(ctx, auditCreate, device.Uuid, requestor, "")

	return connect.NewResponse(&devpb.CreateResponse{
		Device: dev,
	}), nil
}

func (c *DevicesController) Update(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("Update")
	dev := req.Msg
	log.Debug("Update request received", zap.Any("device", dev), zap.Any("context", ctx))

	curr, err := c.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	if curr.Msg.GetAccess().GetLevel() < access.Level_MGMT {
		return nil, apierr.Codef(connect.CodePermissionDenied, "No Access to Device %s", dev.Uuid)
	}

	if dev.Title == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Device Title cannot be empty")
	}

	prevTags := append([]string(nil), curr.Msg.GetTags()...)
	curr.Msg.Tags = dev.Tags
	curr.Msg.Title = dev.Title

	_, err = c.col.ReplaceDocument(ctx, dev.Uuid, curr.Msg)
	if err != nil {
		log.Warn("Error updating Device", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device")
	}

	c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, curr.Msg, "", "")
	added, removed := diffTags(prevTags, curr.Msg.GetTags())
	for _, tag := range added {
		c.publishTag(ctx, shadowpb.ShadowEvent_TAG_ADDED, curr.Msg, tag)
	}
	for _, tag := range removed {
		c.publishTag(ctx, shadowpb.ShadowEvent_TAG_REMOVED, curr.Msg, tag)
	}

	return curr, nil
}

func (c *DevicesController) PatchConfig(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("Patch config")
	dev := req.Msg
	log.Debug("Patch config update request received", zap.Any("device", dev), zap.Any("context", ctx))

	curr, err := c.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	if curr.Msg.GetAccess().GetLevel() < access.Level_ADMIN {
		return nil, apierr.Codef(connect.CodePermissionDenied, "No Access to Device %s", dev.Uuid)
	}

	curr.Msg.Config = dev.Config

	_, err = c.col.ReplaceDocument(ctx, dev.Uuid, curr.Msg)
	if err != nil {
		log.Warn("Error updating Device config", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device config")
	}

	return curr, nil
}

func (c *DevicesController) Toggle(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("Update")
	dev := req.Msg
	log.Debug("Update request received", zap.Any("device", dev), zap.Any("context", ctx))

	curr, err := c.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	if curr.Msg.GetAccess().GetLevel() < access.Level_MGMT {
		return nil, apierr.Codef(connect.CodePermissionDenied, "No Access to Device %s", dev.Uuid)
	}

	res := NewDeviceFromPB(curr.Msg)
	err = c.ica_repo.Toggle(ctx, res, "enabled")
	if err != nil {
		log.Warn("Error updating Device", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device")
	}

	return curr, nil
}

func (c *DevicesController) ToggleBasic(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("Update")
	dev := req.Msg
	log.Debug("Update request received", zap.Any("device", dev), zap.Any("context", ctx))

	curr, err := c.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	if curr.Msg.GetAccess().GetLevel() < access.Level_ADMIN {
		return nil, apierr.Codef(connect.CodePermissionDenied, "Not enough Access to Device %s", dev.Uuid)
	}

	res := NewDeviceFromPB(curr.Msg)
	err = c.ica_repo.Toggle(ctx, res, "basic_enabled")
	if err != nil {
		log.Warn("Error updating Device", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while updating Device")
	}

	return curr, nil
}

func (c *DevicesController) Get(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("Get")
	dev := req.Msg
	log.Debug("Get request received", zap.Any("request", dev), zap.Any("context", ctx))

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	// Getting Account from DB
	// and Check requestor access
	device := *NewBlankDeviceDocument(dev.GetUuid())
	err := c.ica_repo.AccessLevelAndGet(ctx, log, NewBlankAccountDocument(requestor), &device)
	if err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "Account not found or not enough Access Rights")
	}
	if device.Access.Level < access.Level_READ {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}

	token, err := c._MakeToken(map[string]access.Level{
		device.GetUuid(): access.Level_READ,
	}, time.Now().Add(registryDeviceTokenTTL).Unix(), false)
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Failed to issue token")
	}
	device.Token = token

	return connect.NewResponse(device.Device), nil
}

func (c *DevicesController) GetByToken(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("GetByToken")
	dev := req.Msg
	log.Debug("Get by Token request received", zap.String("device", dev.Uuid), zap.Any("context", ctx))

	devices_scope, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("requested device is outside of token scope or not allowed to post"))
	}
	log.Debug("Devices Scope", zap.Any("devices", devices_scope))

	found := false
	for device := range devices_scope {
		if device == dev.GetUuid() {
			found = true
			break
		}
	}
	if !found {
		return nil, apierr.Code(connect.CodeUnauthenticated, "Requested device is outside of token scope")
	}

	var device devpb.Device
	meta, err := c.col.ReadDocument(ctx, dev.GetUuid(), &device)
	if err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "Device not found")
	}
	device.Uuid = meta.ID.Key()
	c.attachNamespace(ctx, &device)

	return connect.NewResponse(&device), nil
}

func (c *DevicesController) List(ctx context.Context, req *connect.Request[pb.QueryRequest]) (*connect.Response[devpb.Devices], error) {
	log := c.log.Named("List")
	q := req.Msg

	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if requestor == "" {
		return nil, apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
	}
	log.Debug("Requestor", zap.String("id", requestor))

	if q.GetNamespace() != "" {
		ctx = WithNamespaceFilter(ctx, q.GetNamespace())
	}

	cr, err := c.ica_repo.ListQuery(ctx, log, NewBlankAccountDocument(requestor), schema.DEVICES_COL)
	if err != nil {
		log.Warn("Error executing query", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Couldn't execute query")
	}
	defer cr.Close()

	var r []*devpb.Device
	for {
		var dev devpb.Device
		meta, err := cr.ReadDocument(ctx, &dev)
		if driver.IsNoMoreDocuments(err) {
			break
		} else if err != nil {
			log.Warn("Error unmarshalling Document", zap.Error(err))
			return nil, apierr.Code(connect.CodeInternal, "Couldn't execute query")
		}
		dev.Uuid = meta.ID.Key()
		if dev.Access.Level < access.Level_MGMT {
			dev.Certificate = nil
		}

		log.Debug("Got document", zap.Any("device", &dev))
		r = append(r, &dev)
	}

	page, next := cutPage(r, q.GetPageSize(), q.GetPageToken())
	return connect.NewResponse(&devpb.Devices{
		Devices:       page,
		NextPageToken: next,
	}), nil
}

func (c *DevicesController) DeviceNamespace(ctx context.Context, id string) (string, error) {
	if c == nil || c.db == nil || id == "" {
		return "", apierr.Code(connect.CodeNotFound, "device not found")
	}
	cr, err := c.db.Query(ctx, `FOR e IN @@edge
FILTER e._to == CONCAT("Devices/", @id)
LIMIT 1
RETURN SPLIT(e._from, "/")[1]`, map[string]interface{}{
		"@edge": schema.NS2DEV, "id": id,
	})
	if err != nil {
		return "", apierr.Code(connect.CodeInternal, "Couldn't execute query")
	}
	defer cr.Close()
	var ns string
	_, err = cr.ReadDocument(ctx, &ns)
	if err != nil || ns == "" {
		return "", apierr.Code(connect.CodeNotFound, "device not found")
	}
	return ns, nil
}

func (c *DevicesController) ListForAddon(ctx context.Context, namespace, tag string) ([]*devpb.Device, error) {
	if c == nil || c.db == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "device registry is not available")
	}
	cr, err := c.db.Query(ctx, `FOR e IN @@edge
FILTER @ns == "" OR e._from == CONCAT("Namespaces/", @ns)
LET d = DOCUMENT(e._to)
FILTER d != null
RETURN {
  uuid: d._key,
  title: d.title,
  tags: d.tags,
  access: { level: @level, namespace: SPLIT(e._from, "/")[1] }
}`, map[string]interface{}{
		"@edge": schema.NS2DEV, "ns": namespace, "level": int32(access.Level_MGMT),
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Couldn't execute query")
	}
	defer cr.Close()
	var out []*devpb.Device
	for {
		var dev devpb.Device
		_, err := cr.ReadDocument(ctx, &dev)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "Couldn't execute query")
		}
		if tag != "" && !hasTag(dev.GetTags(), tag) {
			continue
		}
		out = append(out, &dev)
	}
	return out, nil
}

func hasTag(tags []string, tag string) bool {
	for _, item := range tags {
		if item == tag {
			return true
		}
	}
	return false
}

func (c *DevicesController) Delete(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[pb.DeleteResponse], error) {
	return c.delete(ctx, req, true)
}

func (c *DevicesController) delete(ctx context.Context, _req *connect.Request[devpb.Device], publish bool) (*connect.Response[pb.DeleteResponse], error) {
	log := c.log.Named("Delete")
	req := _req.Msg

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	acc := *NewBlankAccountDocument(requestor)
	dev := *NewBlankDeviceDocument(req.GetUuid())
	err := c.ica_repo.AccessLevelAndGet(ctx, log, &acc, &dev)
	if err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "Account not found or not enough Access Rights")
	}
	if dev.Access.Level < access.Level_ADMIN {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}

	_, err = c.col.RemoveDocument(ctx, dev.ID().Key())
	if err != nil {
		log.Warn("Error removing document", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error while deleting Device")
	}

	err = c.ica_repo.Link(
		ctx, log, c.ns2dev,
		NewBlankNamespaceDocument(*dev.Access.Namespace),
		&dev, access.Level_NONE, access.Role_UNSET,
	)
	if err != nil {
		log.Warn("Error removing device from namespace", zap.Error(err))
	}

	if publish {
		c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_REMOVED, dev.Device, "", "")
	}

	return connect.NewResponse(&pb.DeleteResponse{}), nil
}

const findByFingerprintQuery = `FOR device IN @@devices
FILTER device.certificate.fingerprint == @fingerprint
    OR device.certificate.rollover_fingerprint == @fingerprint
RETURN device`

func fingerprintListed(list [][]byte, fp []byte) bool {
	for _, item := range list {
		if bytes.Equal(item, fp) {
			return true
		}
	}
	return false
}

func (c *DevicesController) GetByFingerprint(ctx context.Context, req *connect.Request[devpb.GetByFingerprintRequest]) (*connect.Response[devpb.Device], error) {
	log := c.log.Named("GetByFingerprint")
	log.Debug("GetByFingerprint request received", zap.Any("request", req), zap.Any("context", ctx))

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	cr, err := c.db.Query(ctx, findByFingerprintQuery, map[string]interface{}{
		"@devices":    schema.DEVICES_COL,
		"fingerprint": req.Msg.GetFingerprint(),
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Error executing query")
	}
	defer cr.Close()

	var r devpb.Device
	meta, err := cr.ReadDocument(ctx, &r)
	if driver.IsNoMoreDocuments(err) {
		return nil, apierr.Code(connect.CodeNotFound, "Device not found")
	}
	if err != nil {
		log.Warn("Error unmarshalling Document", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error executing query")
	}
	r.Uuid = meta.ID.Key()
	if !certificateAccepts(r.Certificate, req.Msg.GetFingerprint()) {
		if r.Certificate != nil && (r.Certificate.Revoked || fingerprintListed(r.Certificate.RevokedFingerprints, req.Msg.GetFingerprint())) {
			return nil, apierr.Code(connect.CodePermissionDenied, "certificate revoked")
		}
		return nil, apierr.Code(connect.CodeNotFound, "Device not found")
	}
	c.attachNamespace(ctx, &r)

	token, err := c._MakeToken(map[string]access.Level{
		r.GetUuid(): access.Level_READ,
	}, time.Now().Add(registryDeviceTokenTTL).Unix(), true)
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Failed to issue token")
	}
	r.Token = token
	c.auditDevice(ctx, auditToken, r.GetUuid(), requestor, jwtID(token))

	return connect.NewResponse(&r), nil
}

func (c *DevicesController) MakeDevicesToken(ctx context.Context, _req *connect.Request[pb.DevicesTokenRequest]) (*connect.Response[pb.TokenResponse], error) {
	log := c.log.Named("MakeDevicesToken")
	req := _req.Msg

	log.Debug("MakeDevicesToken request received", zap.Any("request", req), zap.Any("context", ctx))

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	acc := *NewBlankAccountDocument(requestor)

	if req.GetStream() {
		if len(req.GetDevices()) > 0 {
			return nil, apierr.Code(connect.CodeInvalidArgument, "a streaming token has no device list")
		}
		if req.GetPost() {
			return nil, apierr.Code(connect.CodeInvalidArgument, "a streaming token is read-only")
		}
		ns := req.GetNamespace()
		if ns != "" {
			ok, level := c.ica_repo.AccessLevel(ctx, &acc, NewBlankNamespaceDocument(ns))
			if !ok || level < access.Level_READ {
				return nil, apierr.Codef(connect.CodePermissionDenied, "Not enough Access Rights to namespace: %s", ns)
			}
		}
		token_string, err := c._MakeStreamToken(requestor, ns, requestExpiry(req.GetExpiresAt(), req.GetExp()))
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "Failed to issue token")
		}
		c.auditDevice(ctx, auditToken, "", requestor, jwtID(token_string))
		return connect.NewResponse(&pb.TokenResponse{Token: token_string}), nil
	}

	for uuid := range req.GetDevices() {
		ok, level := c.ica_repo.AccessLevel(ctx, &acc, NewBlankDeviceDocument(uuid))
		if !ok {
			return nil, apierr.Codef(connect.CodeNotFound, "Account not found or not enough Access Rights to device: %s", uuid)
		}
		if level < access.Level_READ {
			return nil, apierr.Codef(connect.CodePermissionDenied, "Not enough Access Rights to device: %s", uuid)
		}
		if req.GetDevices()[uuid] > level {
			return nil, apierr.Codef(connect.CodePermissionDenied, "Not enough Access Rights to device: %s", uuid)
		}
		granted := req.GetDevices()[uuid]
		if granted == access.Level_NONE {
			granted = access.Level_READ
		}
		if req.GetPost() && granted < access.Level_READ {
			granted = access.Level_READ
		}
		req.Devices[uuid] = granted
	}

	exp := requestExpiry(req.GetExpiresAt(), req.GetExp())
	if exp <= 0 {
		exp = time.Now().Add(registryDeviceTokenTTL).Unix()
	}
	token_string, err := c._MakeToken(req.GetDevices(), exp, req.GetPost())
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Failed to issue token")
	}
	jti := jwtID(token_string)
	for id := range req.GetDevices() {
		c.auditDevice(ctx, auditToken, id, requestor, jti)
	}

	return connect.NewResponse(&pb.TokenResponse{Token: token_string}), nil
}

func jwtID(raw string) string {
	parsed, _, err := jwt.NewParser().ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		return ""
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	id, _ := claims[inf.INFINIMESH_JTI_CLAIM].(string)
	return id
}

const registryDeviceTokenTTL = 24 * time.Hour

func (c *DevicesController) _MakeToken(devices map[string]access.Level, exp int64, post bool) (string, error) {
	claims := jwt.MapClaims{}
	claims[inf.INFINIMESH_DEVICES_CLAIM] = devices
	claims[inf.INFINIMESH_JTI_CLAIM] = uuid.NewString()
	if post {
		claims[inf.INFINIMESH_POST_CLAIM] = true
	}
	if exp > 0 {
		claims["exp"] = exp
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(c.SIGNING_KEY)
}

func (c *DevicesController) RevokeDevicesToken(ctx context.Context, req *connect.Request[pb.RevokeDeviceTokenRequest]) (*connect.Response[pb.DeleteResponse], error) {
	id := req.Msg.GetId()
	if id == "" && req.Msg.GetToken() != "" {
		parsed, err := jwt.Parse(req.Msg.GetToken(), func(t *jwt.Token) (interface{}, error) {
			return c.SIGNING_KEY, nil
		})
		if err != nil || !parsed.Valid {
			return nil, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
		}
		claims, _ := parsed.Claims.(jwt.MapClaims)
		jti, _ := claims[inf.INFINIMESH_JTI_CLAIM].(string)
		id = jti
		if exp, ok := claims["exp"].(float64); ok && c.denylist != nil {
			ttl := time.Until(time.Unix(int64(exp), 0))
			if ttl < time.Minute {
				ttl = time.Minute
			}
			if err := c.denylist.Deny(ctx, id, ttl); err != nil {
				return nil, apierr.Code(connect.CodeInternal, "Failed to revoke token")
			}
			return connect.NewResponse(&pb.DeleteResponse{}), nil
		}
	}
	if id == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "token id is required")
	}
	if c.denylist == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "token revocation is not configured")
	}
	if err := c.denylist.Deny(ctx, id, registryDeviceTokenTTL); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "Failed to revoke token")
	}
	return connect.NewResponse(&pb.DeleteResponse{}), nil
}

func (c *DevicesController) _MakeStreamToken(account, namespace string, exp int64) (string, error) {
	claims := jwt.MapClaims{}
	claims[inf.INFINIMESH_ACCOUNT_CLAIM] = account
	claims[inf.INFINIMESH_NOSESSION_CLAIM] = true
	claims[inf.INFINIMESH_STREAM_CLAIM] = true
	if namespace != "" {
		claims[inf.INFINIMESH_NAMESPACE_CLAIM] = namespace
	}
	if exp > 0 {
		claims["exp"] = exp
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(c.SIGNING_KEY)
}

const listDeviceJoinsQuery = `
FOR node, edge, path IN 1 INBOUND @device
GRAPH Permissions
FILTER edge.level > 0 && edge.role != 1
RETURN {
    node: node._id,
    access: KEEP(edge, ["level", "role"])
}
`

func (c *DevicesController) Joins(ctx context.Context, req *connect.Request[devpb.Device]) (*connect.Response[access.Nodes], error) {
	log := c.log.Named("Joins")
	dev := req.Msg
	log.Debug("Fetch Joins request received", zap.String("device", dev.GetUuid()))

	requestor := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor))

	_dev, err := c.Get(ctx, req)
	if err != nil {
		return nil, err
	}
	dev = _dev.Msg

	if dev.Access == nil || dev.Access.Level < access.Level_ADMIN {
		return nil, apierr.Code(connect.CodePermissionDenied, "Must be device Admin to fetch Joins")
	}

	cr, err := c.db.Query(ctx, listDeviceJoinsQuery, map[string]interface{}{
		"device": NewBlankDeviceDocument(dev.Uuid).ID(),
	})
	if err != nil {
		log.Warn("Error querying for joins", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "Error querying for joins")
	}
	defer cr.Close()

	var r []*access.Node
	for {
		var node access.Node
		_, err := cr.ReadDocument(ctx, &node)
		if driver.IsNoMoreDocuments(err) {
			break
		} else if err != nil {
			log.Warn("Error unmarshalling Document", zap.Error(err))
			return nil, apierr.Code(connect.CodeInternal, "Couldn't execute query")
		}
		log.Debug("Got document", zap.Any("node", &node))
		r = append(r, &node)
	}

	return connect.NewResponse(&access.Nodes{Nodes: r}), nil
}

func (c *DevicesController) Join(ctx context.Context, req *connect.Request[pb.JoinGeneralRequest]) (*connect.Response[access.Node], error) {
	log := c.log.Named("Join")

	requestor_id := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	log.Debug("Requestor", zap.String("id", requestor_id))

	requestor := NewBlankAccountDocument(requestor_id)
	dev := NewBlankDeviceDocument(req.Msg.Node)

	err := c.ica_repo.AccessLevelAndGet(ctx, log, requestor, dev)
	if err != nil {
		log.Warn("Error getting Device and access level", zap.Error(err))
		return nil, apierr.Code(connect.CodeNotFound, "Device not found or not enough Access Rights")
	}
	if dev.Access.Role != access.Role_OWNER && dev.Access.Level < access.Level_ADMIN {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}

	var obj InfinimeshGraphNode
	var edge driver.Collection

	col, key := SplitDocID(req.Msg.Join)
	switch col {
	case "Accounts":
		obj = NewBlankAccountDocument(key)
		edge = c.acc2dev
	case "Namespaces":
		obj = NewBlankNamespaceDocument(key)
		edge = c.ns2dev
	}

	if obj == nil {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Unable to determine Object type")
	}

	err = c.ica_repo.AccessLevelAndGet(ctx, log, requestor, obj)
	if err != nil {
		log.Warn("Error getting Object and access level", zap.String("id", req.Msg.Join), zap.Error(err))
		return nil, apierr.Code(connect.CodeNotFound, "Object not found or not enough Access Rights")
	}

	lvl := req.Msg.Access
	if lvl >= access.Level_ADMIN {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Not allowed to share Admin or Root priviliges")
	}

	err = c.ica_repo.Link(ctx, log, edge, obj, dev, req.Msg.Access, access.Role_SHARED)
	if err != nil {
		log.Warn("Error creating edge", zap.Error(err))
		return nil, apierr.Code(connect.CodeInternal, "error creating Permission")
	}

	if col == "Accounts" {
		c.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, dev.Device, "", key)
	}

	return connect.NewResponse(&access.Node{
		Node: req.Msg.Join,
		Access: &access.Access{
			Level: lvl,
		},
	}), nil
}

func (c *DevicesController) publishScope(ctx context.Context, event shadowpb.ShadowEvent, dev *devpb.Device, namespace, account string) {
	if c == nil || c.scope == nil || dev == nil || dev.GetUuid() == "" {
		return
	}
	if namespace == "" {
		namespace = dev.GetAccess().GetNamespace()
	}
	ev := &shadowpb.Shadow{
		Device:    dev.GetUuid(),
		Title:     dev.GetTitle(),
		Namespace: namespace,
		Tags:      append([]string(nil), dev.GetTags()...),
		Event:     event,
		Account:   account,
	}
	if _, err := c.scope.PublishDeviceScope(context.WithoutCancel(ctx), ev); err != nil {
		c.log.Warn("device scope publish failed", zap.String("device", dev.GetUuid()), zap.Error(err))
	}
}

func (c *DevicesController) publishTag(ctx context.Context, event shadowpb.ShadowEvent, dev *devpb.Device, tag string) {
	if c == nil || c.scope == nil || dev == nil || tag == "" {
		return
	}
	ev := &shadowpb.Shadow{
		Device:    dev.GetUuid(),
		Title:     dev.GetTitle(),
		Namespace: dev.GetAccess().GetNamespace(),
		Tags:      append([]string(nil), dev.GetTags()...),
		Event:     event,
		Tag:       tag,
	}
	if _, err := c.scope.PublishDeviceScope(context.WithoutCancel(ctx), ev); err != nil {
		c.log.Warn("tag scope publish failed", zap.String("device", dev.GetUuid()), zap.String("tag", tag), zap.Error(err))
	}
}
