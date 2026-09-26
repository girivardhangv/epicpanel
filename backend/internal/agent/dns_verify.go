package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
)

// VerifyDNS resolves the domain and reports whether it points at this server.
// The RunCloud/Ploi verification model: we do not host DNS; the customer
// points their DNS at the server and we confirm.
func (e *Executor) VerifyDNS(ctx context.Context, domain string) (bool, []string, error) {
	ips, err := net.LookupHost(domain)
	if err != nil {
		return false, nil, fmt.Errorf("resolve %s: %w", domain, err)
	}
	serverIPs, err := localIPs()
	if err != nil {
		return false, ips, fmt.Errorf("enumerate local ips: %w", err)
	}
	// On NAT/EIP hosts (AWS EC2 with an Elastic IP, home labs behind a
	// port-forward) the public IP customers point DNS at is NOT bound to any
	// interface — the machine only sees it as its outbound source address.
	// Include it or every such node reports "DNS not pointing here" forever.
	if pub := outboundIP(); pub != "" {
		serverIPs = append(serverIPs, pub)
	}
	match := false
	for _, rip := range ips {
		for _, sip := range serverIPs {
			if rip == sip {
				match = true
				break
			}
		}
	}
	slog.Info("dns verified", "domain", domain, "resolved", ips, "matches", match)
	return match, ips, nil
}

// outboundIP returns this machine's primary non-loopback IPv4 as seen by the
// internet (the source address of a routed packet). Empty when unroutable —
// callers must tolerate that and rely on interface addresses alone.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && !addr.IP.IsLoopback() && addr.IP.To4() != nil {
		return addr.IP.String()
	}
	return ""
}

func localIPs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	return out, nil
}
