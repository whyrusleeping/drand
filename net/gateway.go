package net

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	//"github.com/drand/drand/protobuf/control"

	"github.com/drand/drand/protobuf/drand"
)

//var DefaultTimeout = time.Duration(30) * time.Second

// PrivateGateway is the main interface to communicate to other drand nodes. It
// acts as a listener to receive incoming requests and acts a client connecting
// to drand particpants.
// The gateway fixes all drand functionalities offered by drand.
type PrivateGateway struct {
	Listener
	ProtocolClient
}

// StartAll starts the control and public functionalities of the node
func (g *PrivateGateway) StartAll() {
	go g.Listener.Start()
}

// StopAll stops the control and public functionalities of the node
func (g *PrivateGateway) StopAll(ctx context.Context) {
	g.Listener.Stop(ctx)
}

// CallOption is simply a wrapper around the grpc options
type CallOption = grpc.CallOption

// Listener is the active listener for incoming requests.
type Listener interface {
	Start()
	Stop(ctx context.Context)
	Addr() string
}

// Service holds all functionalities that a drand node should implement
type Service interface {
	drand.PublicServer
	drand.ControlServer
	drand.ProtocolServer
}

// NewGRPCPrivateGatewayWithoutTLS returns a grpc Gateway listening on "listen" for the
// public methods, listening on "port" for the control methods, using the given
// Service s with the given options.
func NewGRPCPrivateGatewayWithoutTLS(ctx context.Context, listen string, s Service, opts ...grpc.DialOption) (*PrivateGateway, error) {
	l, err := NewGRPCListenerForPrivate(ctx, listen, s)
	if err != nil {
		return nil, err
	}
	return &PrivateGateway{
		ProtocolClient: NewGrpcClient(opts...),
		Listener:       l,
	}, nil
}

// NewGRPCPrivateGatewayWithTLS returns a grpc gateway using the TLS
// certificate manager
func NewGRPCPrivateGatewayWithTLS(ctx context.Context, listen string, certPath, keyPath string, certs *CertManager, s Service, opts ...grpc.DialOption) (*PrivateGateway, error) {
	l, err := NewGRPCListenerForPrivateWithTLS(ctx, listen, certPath, keyPath, s, grpc.ConnectionTimeout(500*time.Millisecond))
	if err != nil {
		return nil, err
	}
	return &PrivateGateway{
		ProtocolClient: NewGrpcClientFromCertManager(certs, opts...),
		Listener:       l,
	}, nil
}

// PublicGateway is the main interface to communicate to users.
// The gateway fixes all drand functionalities offered by drand.
type PublicGateway struct {
	Listener
}

// StartAll starts the control and public functionalities of the node
func (g *PublicGateway) StartAll() {
	go g.Listener.Start()
}

// StopAll stops the control and public functionalities of the node
func (g *PublicGateway) StopAll(ctx context.Context) {
	g.Listener.Stop(ctx)
}

// NewRESTPublicGatewayWithoutTLS returns a grpc Gateway listening on "listen" for the
// public methods, listening on "port" for the control methods, using the given
// Service s with the given options.
func NewRESTPublicGatewayWithoutTLS(ctx context.Context, listen string, handler http.Handler) (*PublicGateway, error) {
	l, err := NewRESTListenerForPublic(ctx, listen, handler)
	if err != nil {
		return nil, err
	}
	return &PublicGateway{Listener: l}, nil
}

// NewRESTPublicGatewayWithTLS returns a grpc gateway using the TLS
// certificate manager
func NewRESTPublicGatewayWithTLS(ctx context.Context, listen string, certPath, keyPath string, certs *CertManager, handler http.Handler) (*PublicGateway, error) {
	l, err := NewRESTListenerForPublicWithTLS(ctx, listen, certPath, keyPath, handler)
	if err != nil {
		return nil, err
	}
	return &PublicGateway{Listener: l}, nil
}
