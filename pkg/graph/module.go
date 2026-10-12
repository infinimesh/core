package graph

import (
	"github.com/arangodb/go-driver"
	"github.com/infinimesh/proto/node/nodeconnect"
	"github.com/infinimesh/proto/pairing"
	"go.uber.org/zap"
)

type DevicesControllerModule interface {
	Handler() nodeconnect.DevicesServiceHandler
	SetSigningKey([]byte)
	SetScopePublisher(ScopePublisher)
	SetDenylist(TokenDenylist)
	SetClaimTokens(ClaimTokens)
}

type devicesControllerModule struct {
	handler *DevicesController
}

func (m *devicesControllerModule) Handler() nodeconnect.DevicesServiceHandler {
	return m.handler
}

func (m *devicesControllerModule) SetSigningKey(key []byte) {
	m.handler.SIGNING_KEY = key
}

func (m *devicesControllerModule) SetScopePublisher(p ScopePublisher) {
	m.handler.SetScopePublisher(p)
}

func (m *devicesControllerModule) SetDenylist(d TokenDenylist) {
	m.handler.SetDenylist(d)
}

func (m *devicesControllerModule) SetClaimTokens(s ClaimTokens) {
	m.handler.SetClaimTokens(s)
}

func NewDevicesControllerModule(log *zap.Logger, db driver.Database,
	hfc pairing.PairingServiceClient) DevicesControllerModule {
	return &devicesControllerModule{
		handler: NewDevicesController(log, db, hfc, NewInfinimeshCommonActionsRepo(db)),
	}
}
