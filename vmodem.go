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
	"time"
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

type RetCode int

const (
	RetCodeOk RetCode = iota
	RetCodeError
	RetCodeSilent
	RetCodeConnect
	RetCodeNoCarrier
	RetCodeNoDialtone
	RetCodeBusy
	RetCodeNoAnswer
	RetCodeRing
	RetCodeSkip
	RetCodeUnknown
)

func CmdReturnFromString(s string) RetCode {
	switch strings.ToUpper(s) {
	case "OK":
		return RetCodeOk
	case "ERROR":
		return RetCodeError
	case "CONNECT":
		return RetCodeConnect
	case "NO CARRIER":
		return RetCodeNoCarrier
	case "NO DIALTONE":
		return RetCodeNoDialtone
	case "BUSY":
		return RetCodeBusy
	case "NO ANSWER":
		return RetCodeNoAnswer
	case "RING":
		return RetCodeRing
	case "SILENT":
		return RetCodeSilent
	case "SKIP":
		return RetCodeSkip
	default:
		return RetCodeUnknown
	}
}

type Modem struct {
	sync.Mutex
	st               ModemStatus
	stCtx            context.Context
	stCtxCancel      context.CancelFunc
	id               string
	tty              io.ReadWriteCloser
	conn             io.ReadWriteCloser
	statusTransition StatusTransitionType
	outgoingCall     OutgoingCallType
	commandHook      CommandHookType
	connectStr       string
	answerChar       string
	sregs            map[byte]byte
	echo             bool
	shortForm        bool
	quietMode        bool
	ringCount        int
	ringMax          int
	disablePreGuard  bool
	disablePostGuard bool
}

type StatusTransitionType func(m *Modem, prevStatus ModemStatus, newStatus ModemStatus)
type OutgoingCallType func(m *Modem, number string) (io.ReadWriteCloser, error)
type CommandHookType func(m *Modem, cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) RetCode

type ModemConfig struct {
	Id               string
	OutgoingCall     OutgoingCallType
	CommandHook      CommandHookType
	StatusTransition StatusTransitionType
	TTY              io.ReadWriteCloser
	ConnectStr       string
	RingMax          int
	AnswerChar       string
	GuardTime        int // 50ms increments
	DisablePreGuard  bool
	DisablePostGuard bool
}

func checkValidCmdChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func checkValidNumChar(b byte) bool {
	return (b >= '0' && b <= '9')
}

func (m *Modem) checkLock() {
	if m.TryLock() {
		panic("Modem lock not held")
	}
}

func (m *Modem) ttyWriteStr(s string) {
	fmt.Fprint(m.tty, s)
}

func (m *Modem) TtyWriteStr(s string) {
	m.checkLock()
	m.ttyWriteStr(s)
}

func (m *Modem) TtyWriteStrSync(s string) {
	m.Lock()
	defer m.Unlock()
	m.ttyWriteStr(s)
}

func (m *Modem) Id() string {
	return m.id
}

func (m *Modem) cr() string {
	if m.shortForm {
		return "\r"
	} else {
		return "\r\n"
	}
}

func (m *Modem) Cr() string {
	m.checkLock()
	return m.cr()
}

func (m *Modem) CrSync() string {
	m.Lock()
	defer m.Unlock()
	return m.cr()
}

func (m *Modem) printRetCode(ret RetCode) {
	retStr := ""
	if m.shortForm {
		switch ret {
		case RetCodeSilent, RetCodeSkip:
			return
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
		case RetCodeRing:
			retStr = "2"
		}
	} else {
		switch ret {
		case RetCodeSilent, RetCodeSkip:
			return
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
		case RetCodeRing:
			retStr = "RING"
		}
	}
	if !m.quietMode {
		m.ttyWriteStr(m.cr() + retStr + m.cr())
	}
}

func (m *Modem) setStatus(status ModemStatus) {
	prevStatus := m.st
	if prevStatus == status {
		return
	}
	if prevStatus == StatusClosed {
		panic(ErrInvalidStateTransition)
	}
	m.stCtxCancel()
	m.stCtx, m.stCtxCancel = context.WithCancel(context.Background())
	m.st = status
	switch m.st {
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
			panic(ErrInvalidStateTransition)
		}
		if prevStatus == StatusRinging && m.answerChar != "" {
			m.conn.Write([]byte(m.answerChar[0:1]))
		}
		m.printRetCode(RetCodeConnect)
		go m.onlineTask(m.stCtx)
	case StatusConnectedCmd:
		if prevStatus != StatusConnected {
			panic(ErrInvalidStateTransition)
		}
		m.printRetCode(RetCodeOk)
	case StatusDialing:
		if prevStatus != StatusIdle {
			panic(ErrInvalidStateTransition)
		}
	case StatusRinging:
		if prevStatus != StatusIdle {
			panic(ErrInvalidStateTransition)
		}
		go m.ringer(m.stCtx)
	case StatusClosed:
		m.tty.Close()
		if prevStatus == StatusConnected || prevStatus == StatusConnectedCmd || prevStatus == StatusRinging {
			m.conn.Close()
			m.conn = nil
		}
	}
	if m.statusTransition != nil {
		m.statusTransition(m, prevStatus, status)
	}
}

func (m *Modem) status() ModemStatus {
	return m.st
}

// Status returns the current status of the modem. Modem lock must be held.
func (m *Modem) Status() ModemStatus {
	m.checkLock()
	return m.status()
}

// StatusSync returns the current status of the modem. Modem lock is acquired and released.
func (m *Modem) StatusSync() ModemStatus {
	m.Lock()
	defer m.Unlock()
	return m.status()
}

func (m *Modem) close() {
	m.setStatus(StatusClosed)
}

// Close closes the modem. Modem lock must be held.
func (m *Modem) Close() {
	m.checkLock()
	m.close()
}

// CloseSync closes the modem. Modem lock is acquired and released.
func (m *Modem) CloseSync() {
	m.Lock()
	defer m.Unlock()
	m.close()
}

func (m *Modem) ringer(ctx context.Context) {
	m.Lock()
	for m.status() == StatusRinging {
		if ctx.Err() != nil {
			break
		}
		m.ringCount++
		m.printRetCode(RetCodeRing)
		if m.ringCount > m.ringMax {
			m.setStatus(StatusIdle)
			break
		}
		if m.sregs[0] > 0 && m.ringCount >= int(m.sregs[0]) {
			m.setStatus(StatusConnected)
			break
		}
		m.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		m.Lock()
	}
	m.ringCount = 0
	m.Unlock()
}

func (m *Modem) onlineTask(ctx context.Context) {
	buff := make([]byte, 128)
	m.Lock()
	for ctx.Err() == nil {
		m.Unlock()
		n, err := m.conn.Read(buff)
		m.Lock()
		if ctx.Err() != nil {
			break
		}
		if err != nil || n == 0 {
			m.setStatus(StatusIdle)
			break
		}
		m.tty.Write(buff[:n])
	}
	m.Unlock()
}

func (m *Modem) incomingCall(conn io.ReadWriteCloser) error {
	if m.status() != StatusIdle {
		return ErrModemBusy
	}
	m.conn = conn
	m.setStatus(StatusRinging)
	return nil
}

// IncomingCall simulates an incoming call. Modem lock must be held.
func (m *Modem) IncomingCall(conn io.ReadWriteCloser) error {
	m.checkLock()
	return m.incomingCall(conn)
}

// IncomingCallSync simulates an incoming call. Modem lock is acquired and released.
func (m *Modem) IncomingCallSync(conn io.ReadWriteCloser) error {
	m.Lock()
	defer m.Unlock()
	return m.incomingCall(conn)
}

func (m *Modem) processDialing(ctx context.Context, number string) {
	if ctx.Err() != nil {
		return
	}
	fail := false
	transport := false
	conn, err := m.outgoingCall(m, number)
	if err != nil {
		fail = true
	} else {
		transport = true
	}
	if m.answerChar != "" && transport {
		buff := make([]byte, 1)
		n, err := conn.Read(buff)
		if err != nil || n != 1 || buff[0] != m.answerChar[0] {
			fail = true
		}
	}
	m.Lock()
	defer m.Unlock()
	if ctx.Err() != nil {
		if transport {
			conn.Close()
		}
		return
	}
	if fail {
		if transport {
			conn.Close()
		}
		m.setStatus(StatusIdle)
		return
	}
	m.conn = conn
	m.setStatus(StatusConnected)
}

func (m *Modem) processCommand(cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) RetCode {
	if m.commandHook != nil {
		r := m.commandHook(m, cmdChar, cmdNum, cmdAssign, cmdQuery, cmdAssignVal)
		if r != RetCodeSkip {
			return r
		}
	}
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
			m.ttyWriteStr(fmt.Sprintf(m.cr()+"%03d\r\n", v))
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
			m.setStatus(StatusDialing)
			number := strings.ToUpper(strings.TrimSpace(cmdAssignVal))
			if len(number) > 0 && (number[0] == 'T' || number[0] == 'P') {
				number = number[1:]
				number = strings.TrimSpace(number)
			}
			go m.processDialing(m.stCtx, number)
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
		m.setStatus(StatusConnected)
		return RetCodeSilent
	case "H":
		if m.status() == StatusConnected || m.status() == StatusConnectedCmd {
			m.setStatus(StatusIdle)
			return RetCodeSilent
		}
	case "O":
		if m.status() != StatusConnectedCmd {
			return RetCodeError
		}
		m.setStatus(StatusConnected)
		return RetCodeSilent
	case "Q":
		n, _ := strconv.Atoi(cmdNum)
		switch n {
		case 0:
			m.quietMode = false
		case 1:
			m.quietMode = true
		default:
			return RetCodeError
		}
	case "&F", "Z":
		m.sregs[0] = 0
		m.echo = true
		m.shortForm = false
		m.quietMode = false
	}
	return RetCodeOk
}

func (m *Modem) processAtCommand(cmd string) RetCode {
	if m.status() != StatusIdle && m.status() != StatusConnectedCmd && m.status() != StatusRinging {
		return RetCodeError
	}
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

			if cmdChar == "" || cmdChar == "&" || cmdChar == "%" {
				if (b == '&' || b == '%') && cmdChar == "" && cmdBuf.Len() > 0 {
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
	return cmdRet
}

func (m *Modem) ProcessAtCommand(cmd string) RetCode {
	m.checkLock()
	return m.processAtCommand(cmd)
}

func (m *Modem) ProcessAtCommandSync(cmd string) RetCode {
	m.Lock()
	defer m.Unlock()
	return m.processAtCommand(cmd)
}

func (m *Modem) ttyReadTask() {
	aFlag := false
	atFlag := false
	buffer := *bytes.NewBuffer(nil)
	byteBuff := make([]byte, 1)
	lastCmd := ""
	plusCnt := 0
	lastPlus := time.Time{}
	lastNotPlus := time.Time{}

	m.Lock()
	for m.status() != StatusClosed {
		m.Unlock()
		n, err := m.tty.Read(byteBuff)
		m.Lock()
		if m.status() == StatusClosed {
			break
		}

		if err != nil || n == 0 {
			m.setStatus(StatusClosed)
			break
		}

		if m.status() == StatusConnected { // online mode pass-through
			if m.conn != nil {
				m.conn.Write(byteBuff)
			}
			if byteBuff[0] == '+' {
				if !m.disablePreGuard {
					if time.Since(lastNotPlus) < time.Duration(m.sregs[12])*50*time.Millisecond {
						plusCnt = 0
						lastNotPlus = time.Now()
						continue
					}
				}

				if time.Since(lastPlus) > time.Duration(m.sregs[12])*50*time.Millisecond {
					plusCnt = 0
				}
				plusCnt++
				lastPlus = time.Now()
				if plusCnt == 3 {
					if m.disablePostGuard {
						m.setStatus(StatusConnectedCmd)
					} else {
						go func(ctx context.Context) {
							time.Sleep(time.Duration(m.sregs[12]) * 50 * time.Millisecond)
							m.Lock()
							defer m.Unlock()
							if ctx.Err() != nil || plusCnt != 3 {
								return
							}
							m.setStatus(StatusConnectedCmd)
						}(m.stCtx)
					}
				}
			} else {
				plusCnt = 0
				lastNotPlus = time.Now()
			}
			continue
		} else {
			plusCnt = 0
		}

		if m.status() == StatusDialing {
			m.setStatus(StatusIdle)
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
				if m.echo {
					m.ttyWriteStr("\r")
				}
				r := m.processAtCommand(lastCmd)
				m.printRetCode(r)
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
					if m.echo {
						m.ttyWriteStr("\x1b[D \x1b[D")
					}
				}
				continue
			}
			if byteBuff[0] == '\r' {
				atFlag = false
				lastCmd = buffer.String()
				if m.echo {
					m.ttyWriteStr("\r")
				}
				r := m.processAtCommand(lastCmd)
				m.printRetCode(r)
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

func NewModem(config *ModemConfig) (*Modem, error) {
	if config == nil {
		return nil, ErrConfigRequired
	}

	if config.TTY == nil {
		return nil, ErrConfigRequired
	}

	m := &Modem{
		st:               StatusIdle,
		id:               config.Id,
		outgoingCall:     config.OutgoingCall,
		commandHook:      config.CommandHook,
		statusTransition: config.StatusTransition,
		tty:              config.TTY,
		connectStr:       config.ConnectStr,
		ringMax:          config.RingMax,
		answerChar:       config.AnswerChar,
		disablePreGuard:  config.DisablePreGuard,
		disablePostGuard: config.DisablePostGuard,
		echo:             true,
		sregs:            make(map[byte]byte),
	}

	m.stCtx, m.stCtxCancel = context.WithCancel(context.Background())

	if m.connectStr == "" {
		m.connectStr = "CONNECT"
	}

	if m.ringMax == 0 {
		m.ringMax = 5
	}

	m.sregs[12] = byte(config.GuardTime)

	go m.ttyReadTask()
	return m, nil
}
