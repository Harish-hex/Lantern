package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/grandcat/zeroconf"
)

const (
	LanternService     = "_lantern._tcp"
	LanternHTTPService = "_lantern-http._tcp"
)

type ServiceInfo struct {
	Instance string
	Host     string
	TCPPort  int
	HTTPPort int
}

func StartAdvertiser(ctx context.Context, info ServiceInfo) (func(), error) {
	instance := strings.TrimSpace(info.Instance)
	if instance == "" {
		instance = "Lantern"
	}

	baseTXT := buildTXTRecords(info)
	services := []*zeroconf.Server{}

	register := func(service string, port int, txt []string) error {
		if port <= 0 {
			return nil
		}
		srv, err := zeroconf.Register(instance, service, "local.", port, txt, nil)
		if err != nil {
			return fmt.Errorf("register %s: %w", service, err)
		}
		services = append(services, srv)
		return nil
	}

	if err := register(LanternService, info.TCPPort, baseTXT); err != nil {
		stopServers(services)
		return nil, err
	}
	if info.HTTPPort > 0 {
		httpTXT := append(append([]string{}, baseTXT...), fmt.Sprintf("role=http"))
		if err := register(LanternHTTPService, info.HTTPPort, httpTXT); err != nil {
			stopServers(services)
			return nil, err
		}
	}

	stop := func() {
		stopServers(services)
	}

	go func() {
		<-ctx.Done()
		stop()
	}()

	return stop, nil
}

func buildTXTRecords(info ServiceInfo) []string {
	txt := []string{
		fmt.Sprintf("tcp_port=%d", info.TCPPort),
		fmt.Sprintf("http_port=%d", info.HTTPPort),
	}
	host := strings.TrimSpace(info.Host)
	if host != "" {
		txt = append(txt, fmt.Sprintf("host=%s", host))
	}
	return txt
}

func stopServers(servers []*zeroconf.Server) {
	for _, srv := range servers {
		if srv != nil {
			srv.Shutdown()
		}
	}
}
