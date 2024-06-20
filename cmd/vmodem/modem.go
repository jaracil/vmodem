package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aymanbagabas/go-pty"
	vm "github.com/jaracil/vmodem"
)

var (
	ttyCount int = 0
)

func outGoingCall(m *vm.Modem, number string) (io.ReadWriteCloser, error) {
	fmt.Printf("Dialing %s\n", number)
	return nil, vm.ErrNoCarrier
}

func main() {
	cmdTTY, err := pty.New()
	if err != nil {
		panic(err)
	}
	defer cmdTTY.Close()
	fmt.Printf("tty path: %s\r\n", cmdTTY.Name())

	m, err := vm.NewModem(context.Background(), &vm.ModemConfig{
		OutgoingCall: outGoingCall,
		TTY:          cmdTTY,
	})

	if err != nil {
		panic(err)
	}
	defer m.Close()
	fmt.Printf("Modem status: %v\n", m.Status())
	for {
		time.Sleep(1 * time.Second)
	}
}
