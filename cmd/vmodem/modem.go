package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aymanbagabas/go-pty"
	vm "github.com/jaracil/vmodem"
	"github.com/jessevdk/go-flags"
)

type Options struct {
	Verbose    []bool `short:"v" long:"verbose" description:"Show verbose debug information"`
	ListenAddr string `short:"a" long:"addr" description:"Listen address" default:"0.0.0.0:2020"`
	TtyPath    string `short:"t" long:"tty" description:"path for TTYs creation" default:"/tmp/vmodem"`
	StartNum   int    `short:"s" long:"start" description:"Start number for TTYs" default:"0"`
	NumTTYs    int    `short:"n" long:"num" description:"Number of TTYs to create" default:"1"`
	RingMax    int    `short:"r" long:"ring" description:"Max number of rings before hangup" default:"10"`
}

var (
	ctx      context.Context
	cancel   context.CancelFunc
	options  Options
	modems   []*vm.Modem
	listener net.Listener
)

func outGoingCall(m *vm.Modem, number string) (io.ReadWriteCloser, error) {
	fmt.Printf("Dialing %s\n", number)
	time.Sleep(5 * time.Second)
	return nil, vm.ErrNoCarrier
}

func commandHook(_ *vm.Modem, cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) vm.CmdReturn {
	fmt.Printf("\r\nCommand with params: cmd:%s num:%s assign:%v query:%v val:%s\n", cmdChar, cmdNum, cmdAssign, cmdQuery, cmdAssignVal)
	return vm.RetCodeSkip
}

func cleanTTYs() {
	for i := 0; i < options.NumTTYs; i++ {
		os.Remove(fmt.Sprintf("%s/tty%d", options.TtyPath, options.StartNum+i))
	}
}

func cleanModems() {
	for i := 0; i < options.NumTTYs; i++ {
		modems[i].CloseSync()
	}
}

func listenTask() {
	// TCP server
	var err error
	listener, err = net.Listen("tcp", options.ListenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating listener: %v\n", err)
		cancel()
		return
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error accepting connection: %v\n", err)
			cancel()
			break
		}
		assigned := false
		// Find a free modem
		for i := 0; i < options.NumTTYs; i++ {
			if err := modems[i].IncomingCallSync(conn); err == nil {
				assigned = true
				break
			}
		}
		if !assigned {
			conn.Close()
			fmt.Fprintf(os.Stderr, "No free modems for incomming call\n")
		}
	}
}

func main() {
	ctx, cancel = context.WithCancel(context.Background())

	gfParser := flags.NewParser(&options, flags.Default)
	if _, err := gfParser.ParseArgs(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	err := os.MkdirAll(options.TtyPath, 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating TTY path: %v\n", err)
		os.Exit(1)
	}

	cleanTTYs()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

	for i := 0; i < options.NumTTYs; i++ {
		tty, err := pty.New()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tty: %v\n", err)
			os.Exit(1)
		}
		m, err := vm.NewModem(&vm.ModemConfig{
			OutgoingCall: outGoingCall,
			CommandHook:  commandHook,
			TTY:          tty,
			RingMax:      options.RingMax,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating modem: %v\n", err)
			os.Exit(1)
		}
		modems = append(modems, m)
		err = os.Symlink(tty.Name(), fmt.Sprintf("%s/tty%d", options.TtyPath, options.StartNum+i))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating symlink: %v\n", err)
			os.Exit(1)
		}
	}

	go listenTask()

	<-ctx.Done()
	if listener != nil {
		listener.Close()
	}
	cleanTTYs()
	cleanModems()
}
