package main

import (
	"os"

	"github.com/creack/pty"
)

// UnixPty is a POSIX compliant Unix pseudo-terminal.
type UnixPty struct {
	master    *os.File
	slaveName string
	closed    bool
}

// Close implements Pty.
func (p *UnixPty) Close() error {
	if p.closed {
		return nil
	}
	defer func() {
		p.closed = true
	}()
	return p.master.Close()
}

// Name implements Pty.
func (p *UnixPty) Name() string {
	return p.slaveName
}

// Read implements Pty.
func (p *UnixPty) Read(b []byte) (n int, err error) {
	return p.master.Read(b)
}

// Control implements UnixPty.
func (p *UnixPty) Control(f func(fd uintptr)) error {
	return p.control(f)
}

func (p *UnixPty) control(f func(fd uintptr)) error {
	conn, err := p.master.SyscallConn()
	if err != nil {
		return err
	}
	return conn.Control(f)
}

// Master implements UnixPty.
func (p *UnixPty) Master() *os.File {
	return p.master
}

// Write implements Pty.
func (p *UnixPty) Write(b []byte) (n int, err error) {
	return p.master.Write(b)
}

// Fd implements Pty.
func (p *UnixPty) Fd() uintptr {
	return p.master.Fd()
}

// NewPty creates a new UnixPty.
func NewPty() (*UnixPty, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}

	// Save the slave name before closing
	slaveName := slave.Name()

	// Close the slave immediately - we don't need to keep it open
	// External processes will open it through the symlink
	slave.Close()

	return &UnixPty{
		master:    master,
		slaveName: slaveName,
	}, nil
}
