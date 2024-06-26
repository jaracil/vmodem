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
	time.Sleep(5 * time.Second)
	return nil, vm.ErrNoCarrier
}

func commandHook(_ *vm.Modem, cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) vm.CmdReturn {
	fmt.Printf("\r\nCommand with params: cmd:%s num:%s assign:%v query:%v val:%s\n", cmdChar, cmdNum, cmdAssign, cmdQuery, cmdAssignVal)
	return vm.RetCodeSkip
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
		CommandHook:  commandHook,
		TTY:          cmdTTY,
	})

	if err != nil {
		panic(err)
	}
	defer m.Close()
	fmt.Printf("Modem status: %v\n", m.StatusSync())
	for {
		time.Sleep(1 * time.Second)
	}
}
