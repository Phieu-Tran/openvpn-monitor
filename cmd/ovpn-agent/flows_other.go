//go:build !linux

package main

import (
	"errors"
	"time"
)

var errNotLinux = errors.New("conntrack events: only on Linux")

func listenCT() (int, error) { return -1, errNotLinux }

func (l *flowLog) capture(fd int, until time.Time) error { return errNotLinux }

func startFlows(l *flowLog) error {
	l.failed(errNotLinux)
	return errNotLinux
}
