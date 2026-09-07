package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func runDaemonCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hivemind daemon <start|stop|status>")
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "start":
		sess, err := connectChecked(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
			return 1
		}
		_ = sess.Close()
		port, _ := readDaemonPort()
		fmt.Printf("hivemindd running on 127.0.0.1:%d\n", port)
		return 0
	case "status":
		if daemonRunning(ctx) {
			port, _ := readDaemonPort()
			fmt.Printf("running (127.0.0.1:%d)\n", port)
			return 0
		}
		fmt.Println("not running")
		return 1
	case "stop":
		return stopDaemon()
	default:
		fmt.Fprintf(os.Stderr, "hivemind daemon: unknown %q\n", args[0])
		return 2
	}
}

func stopDaemon() int {
	b, err := os.ReadFile(pidFilePath())
	if err != nil {
		fmt.Println("not running")
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: bad pid file: %v\n", err)
		return 1
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := signalTerm(proc); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: signalling hivemindd: %v\n", err)
		return 1
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(portFilePath()); os.IsNotExist(err) {
			fmt.Println("stopped")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "hivemind: hivemindd did not stop within 5s")
	return 1
}
