package vmodem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrConfigRequired         = errors.New("config required")
	ErrModemBusy              = errors.New("modem busy")
	ErrInvalidStateTransition = errors.New("invalid state transition")
	ErrNoCarrier              = errors.New("no carrier")
)

// ModemStatus represents the status of the modem
type ModemStatus int

const (
	StatusIdle ModemStatus = iota // Initial state
	StatusDialing
	StatusConnected
	StatusConnectedCmd
	StatusRinging
	StatusClosed // Terminal state, dead modem
)

func (ms ModemStatus) String() string {
	switch ms {
	case StatusIdle:
		return "Idle"
	case StatusDialing:
		return "Dialing"
	case StatusConnected:
		return "Connected"
	case StatusConnectedCmd:
		return "ConnectedCmd"
	case StatusRinging:
		return "Ringing"
	case StatusClosed:
		return "Closed"
	default:
		return "Unknown"
	}
}

type CmdReturn int

const (
	RetCodeOk CmdReturn = iota
	RetCodeError
	RetCodeSilent
	RetCodeConnect
	RetCodeNoCarrier
	RetCodeNoDialtone
	RetCodeBusy
	RetCodeNoAnswer
)

type Modem struct {
	sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	st           ModemStatus
	tty          io.ReadWriteCloser
	conn         io.ReadWriteCloser
	outgoingCall OutgoingCallType
	connectStr   string
	sregs        map[byte]byte
	echo         bool
	shortForm    bool
}

type OutgoingCallType func(m *Modem, number string) (io.ReadWriteCloser, error)

type ModemConfig struct {
	OutgoingCall OutgoingCallType
	TTY          io.ReadWriteCloser
	ConnectStr   string
}

func checkValidCmdChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func checkValidNumChar(b byte) bool {
	return (b >= '0' && b <= '9')
}

func (m *Modem) TtyWriteStrAsync(s string) {
	m.Lock()
	defer m.Unlock()
	if m.st != StatusConnected {
		m.TtyWriteStr(s)
	}
}

func (m *Modem) TtyWriteStr(s string) {
	fmt.Fprint(m.tty, s)
}

func (m *Modem) Cr() string {
	if m.shortForm {
		return "\r"
	} else {
		return "\r\n"
	}
}

func (m *Modem) printRetCode(ret CmdReturn) {
	if ret == RetCodeSilent {
		return
	}
	retStr := ""
	if m.shortForm {
		switch ret {
		case RetCodeOk:
			retStr = "0"
		case RetCodeError:
			retStr = "4"
		case RetCodeConnect:
			retStr = "1"
		case RetCodeNoCarrier:
			retStr = "3"
		case RetCodeNoDialtone:
			retStr = "6"
		case RetCodeBusy:
			retStr = "7"
		case RetCodeNoAnswer:
			retStr = "8"
		}
	} else {
		switch ret {
		case RetCodeOk:
			retStr = "OK"
		case RetCodeError:
			retStr = "ERROR"
		case RetCodeConnect:
			retStr = m.connectStr
		case RetCodeNoCarrier:
			retStr = "NO CARRIER"
		case RetCodeNoDialtone:
			retStr = "NO DIALTONE"
		case RetCodeBusy:
			retStr = "BUSY"
		case RetCodeNoAnswer:
			retStr = "NO ANSWER"
		}
	}
	m.TtyWriteStr(m.Cr() + retStr + m.Cr())
}

func (m *Modem) setStatus(status ModemStatus) error {
	prevStatus := m.st
	if prevStatus == StatusClosed {
		return ErrInvalidStateTransition
	}
	switch status {
	case StatusIdle:
		if prevStatus == StatusConnected || prevStatus == StatusConnectedCmd || prevStatus == StatusDialing {
			m.printRetCode(RetCodeNoCarrier)
		}
		if prevStatus == StatusConnected || prevStatus == StatusConnectedCmd || prevStatus == StatusRinging {
			m.conn.Close()
			m.conn = nil
		}

	case StatusConnected:
		if prevStatus != StatusDialing && prevStatus != StatusRinging && prevStatus != StatusConnectedCmd {
			return ErrInvalidStateTransition
		}
		m.printRetCode(RetCodeConnect)
	case StatusConnectedCmd:
		if prevStatus != StatusConnected {
			return ErrInvalidStateTransition
		}
		m.printRetCode(RetCodeOk)
	case StatusDialing:
		if prevStatus != StatusIdle {
			return ErrInvalidStateTransition
		}
	case StatusRinging:
		if prevStatus != StatusIdle {
			return ErrInvalidStateTransition
		}
	}
	m.st = status
	fmt.Printf("Modem status transition: %v -> %v\n", prevStatus, status)
	return nil
}

func (m *Modem) status() ModemStatus {
	return m.st
}

func (m *Modem) Status() ModemStatus {
	m.RLock()
	defer m.RUnlock()
	return m.status()
}

func (m *Modem) close() {
	m.setStatus(StatusClosed)
	m.cancel()
	m.tty.Close()
}

func (m *Modem) Close() {
	m.close()
}

func (m *Modem) IncomingCall(conn io.ReadWriteCloser) error {
	m.Lock()
	defer m.Unlock()
	if m.status() != StatusIdle {
		return ErrModemBusy
	}
	err := m.setStatus(StatusRinging)
	if err != nil {
		return err
	}
	m.conn = conn
	return nil
}

func (m *Modem) processCommand(cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) CmdReturn {
	fmt.Printf("\r\nCommand with params: cmd:%s num:%s assign:%v query:%v val:%s\n", cmdChar, cmdNum, cmdAssign, cmdQuery, cmdAssignVal)

	switch cmdChar {
	case "S":
		r, _ := strconv.Atoi(cmdNum)
		if r < 0 || r > 255 {
			return RetCodeError
		}
		if cmdAssign {
			v, _ := strconv.Atoi(cmdAssignVal)
			if v < 0 || v > 255 {
				return RetCodeError
			}
			m.sregs[byte(r)] = byte(v)
			return RetCodeOk
		}
		if cmdQuery {
			v := m.sregs[byte(r)]
			m.TtyWriteStr(fmt.Sprintf(m.Cr()+"%03d\r\n", v))
			return RetCodeOk
		}
	case "E":
		n, _ := strconv.Atoi(cmdNum)
		switch n {
		case 0:
			m.echo = false
		case 1:
			m.echo = true
		default:
			return RetCodeError
		}
	case "V":
		n, _ := strconv.Atoi(cmdNum)
		switch n {
		case 0:
			m.shortForm = true
		case 1:
			m.shortForm = false
		default:
			return RetCodeError
		}
	case "D":
		if m.status() != StatusIdle {
			return RetCodeError
		}
		if m.outgoingCall != nil {
			err := m.setStatus(StatusDialing)
			if err != nil {
				return RetCodeError
			}
			conn, err := m.outgoingCall(m, cmdAssignVal)
			if err != nil {
				m.setStatus(StatusIdle)
				return RetCodeSilent
			}
			m.conn = conn
			err = m.setStatus(StatusConnected)
			if err != nil {
				return RetCodeError
			}
			return RetCodeSilent
		}
		return RetCodeNoCarrier
	case "A":
		if m.status() == StatusIdle {
			return RetCodeNoCarrier
		}
		if m.status() != StatusRinging {
			return RetCodeError
		}
		err := m.setStatus(StatusConnected)
		if err != nil {
			return RetCodeError
		}
		return RetCodeSilent
	case "H":
		if m.status() == StatusConnected || m.status() == StatusConnectedCmd {
			err := m.setStatus(StatusIdle)
			if err != nil {
				return RetCodeError
			}
			return RetCodeSilent
		}
	case "O":
		if m.status() != StatusConnectedCmd {
			return RetCodeError
		}
		err := m.setStatus(StatusConnected)
		if err != nil {
			return RetCodeError
		}
		return RetCodeSilent
	}
	return RetCodeOk
}

func (m *Modem) processAtCommand(cmd string) {
	fmt.Printf("\r\nAT command received: \"%s\"\r\n", cmd)
	cmdBuf := bytes.NewBufferString(cmd)
	cmdRet := RetCodeOk
	e := false
	for cmdBuf.Len() > 0 && !e {
		cmdChar := ""
		cmdNum := ""
		cmdLong := false
		cmdAssign := false
		cmdQuery := false
		cmdAssignVal := ""

		for cmdBuf.Len() > 0 && !e {
			b, err := cmdBuf.ReadByte()
			if err != nil {
				e = true
				break
			}

			if b == '?' {
				if cmdChar != "" {
					cmdQuery = true
					break
				} else {
					e = true
					break
				}
			}

			if cmdAssign {
				if !cmdLong && !checkValidNumChar(b) { // short command only accepts numbers
					cmdBuf.UnreadByte()
					break
				}
				cmdAssignVal += string(b)
				continue
			}

			if b == '+' || b == '#' {
				if cmdChar == "" {
					cmdLong = true
					cmdChar += string(b)
					continue
				} else {
					e = true
					break
				}
			}

			if b == '=' {
				if cmdChar != "" {
					cmdAssign = true
					continue
				} else {
					e = true
					break
				}
			}

			if cmdLong {
				if checkValidCmdChar(b) {
					cmdChar += string(b)
					continue
				} else {
					e = true
					break
				}
			}

			if cmdChar == "" || cmdChar == "&" {
				if b == '&' && cmdChar == "" && cmdBuf.Len() > 0 {
					cmdChar += string(b)
					continue
				}
				if checkValidCmdChar(b) {
					cmdChar += string(b)
					if cmdChar == "d" || cmdChar == "D" {
						cmdLong = true
						cmdAssign = true
					}
				} else {
					e = true
					break
				}
			} else {
				if checkValidNumChar(b) {
					cmdNum += string(b)
				} else {
					cmdBuf.UnreadByte()
					break
				}
			}
		}
		if !e {
			cmdRet = m.processCommand(strings.ToUpper(cmdChar), cmdNum, cmdAssign, cmdQuery, cmdAssignVal)
			if cmdRet == RetCodeError {
				break
			}
		}
		if cmdLong {
			break // long commands don't support chaining
		}
	}

	if e {
		cmdRet = RetCodeError
	}
	m.printRetCode(cmdRet)
}

func (m *Modem) ttyReadTask() {
	aFlag := false
	atFlag := false
	buffer := *bytes.NewBuffer(nil)
	byteBuff := make([]byte, 1)
	lastCmd := ""
	m.Lock()
	for {
		if m.ctx.Err() != nil {
			break
		}
		m.Unlock()
		n, err := m.tty.Read(byteBuff)
		m.Lock()
		if err != nil || n == 0 {
			break
		}
		if m.status() == StatusConnected { // online mode pass-through
			if m.conn != nil {
				m.conn.Write(byteBuff)
			}
			continue
		}

		if !atFlag {
			if m.echo {
				m.tty.Write(byteBuff)
			}
			if bytes.ToUpper(byteBuff)[0] == 'A' {
				aFlag = true
				continue
			}
			if aFlag && byteBuff[0] == '/' {
				aFlag = false
				m.processAtCommand(lastCmd)
				continue
			}
			if aFlag && bytes.ToUpper(byteBuff)[0] == 'T' {
				atFlag = true
				aFlag = false
				continue
			}
			aFlag = false
		} else {
			if byteBuff[0] == 0x7f {
				if buffer.Len() > 0 {
					buffer.Truncate(buffer.Len() - 1)
					m.TtyWriteStr("\x1b[D \x1b[D")
				}
				continue
			}
			if byteBuff[0] == '\r' {
				atFlag = false
				lastCmd = buffer.String()
				m.processAtCommand(lastCmd)
				buffer.Reset()
				continue
			}
			if buffer.Len() < 100 && strconv.IsPrint(rune(byteBuff[0])) {
				buffer.Write(byteBuff)
				if m.echo {
					m.tty.Write(byteBuff)
				}
			}
		}
	}
	m.Unlock()
}

func NewModem(ctx context.Context, config *ModemConfig) (*Modem, error) {
	if config == nil {
		return nil, ErrConfigRequired
	}

	if config.TTY == nil {
		return nil, ErrConfigRequired
	}

	modemContext, modemCancel := context.WithCancel(ctx)
	m := &Modem{
		ctx:          modemContext,
		cancel:       modemCancel,
		st:           StatusIdle,
		outgoingCall: config.OutgoingCall,
		tty:          config.TTY,
		connectStr:   config.ConnectStr,
		echo:         true,
		sregs:        make(map[byte]byte),
	}

	if m.connectStr == "" {
		m.connectStr = "CONNECT"
	}

	go m.ttyReadTask()
	return m, nil
}
