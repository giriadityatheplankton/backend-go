//go:build integration

package integration_test

import "net"

type tcpAddr = net.TCPAddr

func netListen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
