package main

// forge-go: one process serving git smart HTTP, REST v3, GraphQL and MCP from memory. It binds an
// ephemeral port and prints "PORT <n>" on stdout; main.ts registers that port with the suite.

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
)

// listener stays referenced so its finalizer never closes the socket the event loop accepts on.
var listener net.Listener

func main() {
	// One P keeps the sequential workloads on one thread with no spinning hand-offs, and the
	// collector waits for the soft memory limit: a trial allocates far less than the pod holds.
	procs := 1
	if v, err := strconv.Atoi(os.Getenv("FORGE_PROCS")); err == nil && v > 0 {
		procs = v
	}
	runtime.GOMAXPROCS(procs)
	debug.SetGCPercent(-1)
	debug.SetMemoryLimit(600 << 20)
	initStore()
	addr := os.Getenv("FORGE_ADDR")
	if addr == "" {
		addr = ":0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	listener = ln
	raw, err := ln.(*net.TCPListener).SyscallConn()
	if err != nil {
		fmt.Fprintln(os.Stderr, "listener:", err)
		os.Exit(1)
	}
	lfd := -1
	raw.Control(func(fd uintptr) { lfd = int(fd) })
	fmt.Printf("PORT %d\n", ln.Addr().(*net.TCPAddr).Port)
	serve(lfd)
}
