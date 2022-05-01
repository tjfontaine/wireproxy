package wireproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"

	"github.com/things-go/go-socks5"

	"golang.zx2c4.com/go118/netip"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// errorLogger is the logger to print error message
var errorLogger = log.New(os.Stderr, "ERROR: ", log.LstdFlags)

// VirtualTun stores a reference to netstack network and DNS configuration
type VirtualTun struct {
	tnet      *netstack.Net
	systemDNS bool
}

// RoutineSpawner spawns a routine (e.g. socks5, tcp static routes) after the configuration is parsed
type RoutineSpawner interface {
	SpawnRoutine(vt *VirtualTun)
}

type addressPort struct {
	address string
	port    uint16
}

// LookupAddr lookups a hostname.
// DNS traffic may or may not be routed depending on VirtualTun's setting
func (d VirtualTun) LookupAddr(ctx context.Context, name string) ([]string, error) {
	if d.systemDNS {
		return net.DefaultResolver.LookupHost(ctx, name)
	} else {
		return d.tnet.LookupContextHost(ctx, name)
	}
}

// ResolveAddrPortWithContext resolves a hostname and returns an AddrPort.
// DNS traffic may or may not be routed depending on VirtualTun's setting
func (d VirtualTun) ResolveAddrWithContext(ctx context.Context, name string) (*netip.Addr, error) {
	addrs, err := d.LookupAddr(ctx, name)
	if err != nil {
		return nil, err
	}

	size := len(addrs)
	if size == 0 {
		return nil, errors.New("no address found for: " + name)
	}

	rand.Shuffle(size, func(i, j int) {
		addrs[i], addrs[j] = addrs[j], addrs[i]
	})

	var addr netip.Addr
	for _, saddr := range addrs {
		addr, err = netip.ParseAddr(saddr)
		if err == nil {
			break
		}
	}

	if err != nil {
		return nil, err
	}

	return &addr, nil
}

var logger = log.New(os.Stdout, "socks5: ", log.LstdFlags)

// Resolve resolves a hostname and returns an IP.
// DNS traffic may or may not be routed depending on VirtualTun's setting
func (d VirtualTun) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	logger.Printf("resolve: %+v\n", name)
	addr, err := d.ResolveAddrWithContext(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	return ctx, addr.AsSlice(), nil
}

func parseAddressPort(endpoint string) (*addressPort, error) {
	name, sport, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, err
	}

	port, err := strconv.Atoi(sport)
	if err != nil || port < 0 || port > 65535 {
		return nil, &net.OpError{Op: "dial", Err: errors.New("port must be numeric")}
	}

	return &addressPort{address: name, port: uint16(port)}, nil
}

func (d VirtualTun) resolveToAddrPort(endpoint *addressPort) (*netip.AddrPort, error) {
	addr, err := d.ResolveAddrWithContext(context.Background(), endpoint.address)
	if err != nil {
		return nil, err
	}

	addrPort := netip.AddrPortFrom(*addr, endpoint.port)
	return &addrPort, nil
}

func handleBind(ctx context.Context, writer io.Writer, request *socks5.Request) error {
	logger.Printf("bind: %+v\n", request)
	return fmt.Errorf("bind isn't implemented")
}

// Spawns a socks5 server.
func (config *Socks5Config) SpawnRoutine(vt *VirtualTun) {
	opts := []socks5.Option{
		socks5.WithResolver(vt),
		socks5.WithDial(vt.tnet.DialContext),
		socks5.WithBindHandle(handleBind),
		socks5.WithRule(socks5.NewPermitAll()),
		socks5.WithLogger(socks5.NewLogger(logger)),
	}

	if username := config.Username; username != "" {
		credentials := socks5.StaticCredentials{}
		credentials[username] = config.Password
		opts = append(opts, socks5.WithCredential(credentials))
	}

	server := socks5.NewServer(opts...)

	if err := server.ListenAndServe("tcp", config.BindAddress); err != nil {
		log.Fatal(err)
	}
}

// connForward copy data from `from` to `to`, then close both stream.
func connForward(bufSize int, from io.ReadWriteCloser, to io.ReadWriteCloser) {
	buf := make([]byte, bufSize)
	_, err := io.CopyBuffer(to, from, buf)
	if err != nil {
		errorLogger.Printf("Cannot forward traffic: %s\n", err.Error())
	}
	_ = from.Close()
	_ = to.Close()
}

// tcpClientForward starts a new connection via wireguard and forward traffic from `conn`
func tcpClientForward(vt *VirtualTun, raddr *addressPort, conn net.Conn) {
	target, err := vt.resolveToAddrPort(raddr)
	if err != nil {
		errorLogger.Printf("TCP Server Tunnel to %s: %s\n", target, err.Error())
		return
	}

	tcpAddr := TCPAddrFromAddrPort(*target)

	sconn, err := vt.tnet.DialTCP(tcpAddr)
	if err != nil {
		errorLogger.Printf("TCP Client Tunnel to %s: %s\n", target, err.Error())
		return
	}

	go connForward(1024, sconn, conn)
	go connForward(1024, conn, sconn)
}

// Spawns a local TCP server which acts as a proxy to the specified target
func (conf *TCPClientTunnelConfig) SpawnRoutine(vt *VirtualTun) {
	raddr, err := parseAddressPort(conf.Target)
	if err != nil {
		log.Fatal(err)
	}

	server, err := net.ListenTCP("tcp", conf.BindAddress)
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := server.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go tcpClientForward(vt, raddr, conn)
	}
}

// tcpServerForward starts a new connection locally and forward traffic from `conn`
func tcpServerForward(vt *VirtualTun, raddr *addressPort, conn net.Conn) {
	target, err := vt.resolveToAddrPort(raddr)
	if err != nil {
		errorLogger.Printf("TCP Server Tunnel to %s: %s\n", target, err.Error())
		return
	}

	tcpAddr := TCPAddrFromAddrPort(*target)

	logger.Printf("Forward %s:%d -> %s:%d\n", raddr.address, raddr.port, tcpAddr.IP, tcpAddr.Port)

	sconn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		errorLogger.Printf("TCP Server Tunnel to %s: %s\n", target, err.Error())
		return
	}

	go connForward(1024, sconn, conn)
	go connForward(1024, conn, sconn)
}

// Spawns a TCP server on wireguard which acts as a proxy to the specified target
func (conf *TCPServerTunnelConfig) SpawnRoutine(vt *VirtualTun) {
	raddr, err := parseAddressPort(conf.Target)
	if err != nil {
		log.Fatal(err)
	}

	addr := &net.TCPAddr{Port: conf.ListenPort}
	server, err := vt.tnet.ListenTCP(addr)
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := server.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go tcpServerForward(vt, raddr, conn)
	}
}
