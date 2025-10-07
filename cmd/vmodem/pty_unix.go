package main

import (
	"errors"
	"os"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// UnixPty is a POSIX compliant Unix pseudo-terminal.
type UnixPty struct {
	master, slave *os.File
	closed        bool
}

// Close implements Pty.
func (p *UnixPty) Close() error {
	if p.closed {
		return nil
	}
	defer func() {
		p.closed = true
	}()
	return errors.Join(p.master.Close(), p.slave.Close())
}

// Name implements Pty.
func (p *UnixPty) Name() string {
	return p.slave.Name()
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

// Slave implements UnixPty.
func (p *UnixPty) Slave() *os.File {
	return p.slave
}

// Write implements Pty.
func (p *UnixPty) Write(b []byte) (n int, err error) {
	return p.master.Write(b)
}

// Fd implements Pty.
func (p *UnixPty) Fd() uintptr {
	return p.master.Fd()
}

// IsSlaveClosed checks if the slave end has no readers/writers.
func (p *UnixPty) IsSlaveClosed() (bool, error) {
	fds := []unix.PollFd{{
		Fd:     int32(p.master.Fd()),
		Events: unix.POLLOUT,
	}}

	_, err := unix.Poll(fds, 0) // No wait
	if err != nil {
		return false, err
	}

	// POLLHUP indicates that the slave has no processes with it open
	return (fds[0].Revents & unix.POLLHUP) != 0, nil
}

// NewPty creates a new UnixPty.
func NewPty() (*UnixPty, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}

	return &UnixPty{
		master: master,
		slave:  slave,
	}, nil
}
