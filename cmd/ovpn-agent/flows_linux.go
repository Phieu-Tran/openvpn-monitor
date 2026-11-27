package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// listenCT subscribes to the kernel's "new connection" events. Read only: the socket
// never sends a request. Needs CAP_NET_ADMIN, in the network namespace of the VPN
// traffic (the host's: OpenVPN and the agent both run with network_mode: host).
func listenCT() (int, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_NETFILTER)
	if err != nil {
		return -1, err
	}
	// A large buffer rides out bursts; FORCE goes past rmem_max (CAP_NET_ADMIN).
	if syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, 8<<20) != nil {
		syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 8<<20)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: 1 /* NFNLGRP_CONNTRACK_NEW */}); err != nil {
		syscall.Close(fd)
		if errors.Is(err, syscall.EPERM) {
			return -1, fmt.Errorf("conntrack events: %w (the agent needs cap_add: [NET_ADMIN])", err)
		}
		return -1, fmt.Errorf("conntrack events: %w", err)
	}
	return fd, nil
}

// capture feeds new connections into l until the socket fails; until is zero = forever.
func (l *flowLog) capture(fd int, until time.Time) error {
	if !until.IsZero() {
		d := time.Until(until)
		tv := syscall.NsecToTimeval(d.Nanoseconds())
		syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	}
	buf := make([]byte, 1<<16)
	for until.IsZero() || time.Now().Before(until) {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		switch {
		case errors.Is(err, syscall.ENOBUFS): // the kernel dropped events: we were too slow
			l.dropped.Add(1)
			continue
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EAGAIN) && !until.IsZero():
			return nil
		case err != nil:
			return err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			continue
		}
		now := time.Now()
		for _, m := range msgs {
			if m.Header.Type != ctMsgNew {
				continue
			}
			if src, dst, port, proto, ok := parseCT(m.Data); ok {
				l.add(now, src, dst, port, proto)
			}
		}
	}
	return nil
}

// startFlows runs the capture in the background; a failure is kept and reported by
// /api/v1/flows and -check, the rest of the agent keeps working.
func startFlows(l *flowLog) error {
	fd, err := listenCT()
	if err != nil {
		l.failed(err)
		return err
	}
	go func() {
		err := l.capture(fd, time.Time{})
		l.failed(fmt.Errorf("conntrack events stopped: %w", err))
	}()
	return nil
}
