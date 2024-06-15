package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aymanbagabas/go-pty"
	vm "github.com/jaracil/vmodem"
)

func main() {
	cmdTTY, err := pty.New()
	if err != nil {
		panic(err)
	}
	defer cmdTTY.Close()
	fmt.Printf("tty path: %s\r\n", cmdTTY.Name())

	m, err := vm.NewModem(context.Background(), &vm.ModemConfig{
		OutgoingCb: nil,
		TTY:        cmdTTY,
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
