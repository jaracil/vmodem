// Package vmodem provides a virtual modem implementation that simulates
// Hayes-compatible modems over TCP/IP networks. It creates virtual TTY devices
// and provides modem functionality for legacy systems that need to communicate
// over modern networks.
//
// The core component is the Modem struct which implements a state machine
// with the following states: Idle, Dialing, Connected, ConnectedCmd, Ringing,
// and Closed. The modem supports standard AT commands, phone number translation,
// and extensible hooks for custom command processing.
//
// Example usage:
//
//	config := &ModemConfig{
//		Id:           "tty0",
//		TTY:          ttyDevice,
//		OutgoingCall: dialFunc,
//		ConnectStr:   "CONNECT",
//		RingMax:      5,
//	}
//	modem, err := NewModem(config)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer modem.CloseSync()
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
	// ErrConfigRequired is returned when a required configuration parameter is missing
	ErrConfigRequired = errors.New("config required")
	// ErrModemBusy is returned when attempting to use a modem that is already in use
	ErrModemBusy = errors.New("modem busy")
	// ErrInvalidStateTransition is returned when an invalid state transition is attempted
	ErrInvalidStateTransition = errors.New("invalid state transition")
	// ErrNoCarrier is returned when no network connection can be established
	ErrNoCarrier = errors.New("no carrier")
)

// ModemStatus represents the current operational state of the modem.
// The modem follows a strict state machine with defined transitions.
type ModemStatus int

const (
	// StatusIdle represents the initial idle state where the modem is ready for commands
	StatusIdle ModemStatus = iota
	// StatusDialing represents the state when the modem is attempting an outgoing connection
	StatusDialing
	// StatusConnected represents the state when the modem has an active data connection
	StatusConnected
	// StatusConnectedCmd represents the state when the modem is in command mode during an active connection
	StatusConnectedCmd
	// StatusRinging represents the state when the modem is receiving an incoming call
	StatusRinging
	// StatusClosed represents the terminal state where the modem is permanently closed
	StatusClosed
)

// String returns a human-readable string representation of the modem status.
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

// RetCode represents the return code for AT command processing.
// These codes correspond to standard Hayes modem response codes.
type RetCode int

const (
	// RetCodeOk indicates successful command execution
	RetCodeOk RetCode = iota
	// RetCodeError indicates command execution failed
	RetCodeError
	// RetCodeSilent indicates no response should be sent
	RetCodeSilent
	// RetCodeConnect indicates a successful connection was established
	RetCodeConnect
	// RetCodeNoCarrier indicates no network connection is available
	RetCodeNoCarrier
	// RetCodeNoDialtone indicates no dial tone was detected
	RetCodeNoDialtone
	// RetCodeBusy indicates the remote endpoint is busy
	RetCodeBusy
	// RetCodeNoAnswer indicates the remote endpoint did not answer
	RetCodeNoAnswer
	// RetCodeRing indicates an incoming call is being received
	RetCodeRing
	// RetCodeSkip indicates the command should be skipped and processed by default handler
	RetCodeSkip
	// RetCodeUnknown indicates an unrecognized return code
	RetCodeUnknown
)

// CmdReturnFromString converts a string representation of a modem response
// to its corresponding RetCode. It performs case-insensitive matching.
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

// Modem represents a virtual Hayes-compatible modem that bridges TTY interfaces
// with TCP/IP networks. It implements a complete modem state machine with support
// for AT commands, phone number translation, and extensible hooks.
//
// The modem is thread-safe and uses a mutex to protect internal state.
// Most operations require the caller to hold the modem lock, with Sync variants
// available for convenience that acquire and release the lock automatically.
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
	lineHook         LineHookType
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
	metrics          *Metrics
}

// StatusTransitionType defines a callback function that is called whenever the modem
// changes state. It receives the modem instance and both the previous and new status.
type StatusTransitionType func(m *Modem, prevStatus ModemStatus, newStatus ModemStatus)

// OutgoingCallType defines a callback function for handling outgoing calls.
// It receives the modem instance and phone number, and should return a connection
// or an error if the call cannot be established.
type OutgoingCallType func(m *Modem, number string) (io.ReadWriteCloser, error)

// CommandHookType defines a callback function for handling custom AT commands.
// It receives the modem instance, command character, numeric parameter, and flags
// indicating if it's an assignment or query. It should return a RetCode indicating
// how the command should be processed.
type CommandHookType func(m *Modem, cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) RetCode

// LineHookType defines a callback function for handling complete command lines.
// It receives the modem instance and the complete command line. It should return
// a RetCode indicating how the line should be processed.
type LineHookType func(m *Modem, line string) RetCode

// ModemConfig contains the configuration parameters for creating a new modem instance.
// The Id and TTY fields are required, while other fields have reasonable defaults.
type ModemConfig struct {
	// Id is a unique identifier for the modem instance
	Id string
	// OutgoingCall is an optional callback for handling outgoing calls
	OutgoingCall OutgoingCallType
	// CommandHook is an optional callback for handling custom AT commands
	CommandHook CommandHookType
	// LineHook is an optional callback for handling complete command lines
	LineHook LineHookType
	// StatusTransition is an optional callback for status change notifications
	StatusTransition StatusTransitionType
	// TTY is the terminal device interface (required)
	TTY io.ReadWriteCloser
	// ConnectStr is the string sent when a connection is established (default: "CONNECT")
	ConnectStr string
	// RingMax is the maximum number of rings before hanging up (default: 5)
	RingMax int
	// AnswerChar is an optional character sent when answering a call
	AnswerChar string
	// GuardTime is the guard time for +++ escape sequence in 50ms increments (default: 20)
	GuardTime int
	// DisablePreGuard disables the pre-guard time check for +++ escape sequence
	DisablePreGuard bool
	// DisablePostGuard disables the post-guard time check for +++ escape sequence
	DisablePostGuard bool
}

// Metrics contains runtime statistics and performance information for a modem instance.
// All byte counters are cumulative totals since the modem was created.
type Metrics struct {
	// Status is the current operational status of the modem
	Status ModemStatus
	// TtyTxBytes is the total number of bytes transmitted to the TTY
	TtyTxBytes int
	// TtyRxBytes is the total number of bytes received from the TTY
	TtyRxBytes int
	// ConnTxBytes is the total number of bytes transmitted to network connections (online mode)
	ConnTxBytes int
	// ConnRxBytes is the total number of bytes received from network connections (online mode)
	ConnRxBytes int
	// NumConns is the total number of connections handled
	NumConns int
	// NumInConns is the total number of incoming connections handled
	NumInConns int
	// NumOutConns is the total number of outgoing connections handled
	NumOutConns int
	// LastTtyTxTime is the timestamp of the last TTY transmission
	LastTtyTxTime time.Time
	// LastTtyRxTime is the timestamp of the last TTY reception
	LastTtyRxTime time.Time
	// LastAtCmdTime is the timestamp of the last AT command processed
	LastAtCmdTime time.Time
	// LastConnTime is the timestamp of the last connection establishment
	LastConnTime time.Time
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

func (m *Modem) ttyWrite(b []byte) {
	m.metrics.LastTtyTxTime = time.Now()
	n, err := m.tty.Write(b)
	if err != nil || n == 0 {
		m.setStatus(StatusClosed)
		return
	}
	m.metrics.TtyTxBytes += n
}

func (m *Modem) ttyWriteStr(s string) {
	m.ttyWrite([]byte(s))
}

// TtyWriteStr writes a string to the TTY device.
// The modem lock must be held before calling this method.
// Use TtyWriteStrSync for automatic lock management.
func (m *Modem) TtyWriteStr(s string) {
	m.checkLock()
	m.ttyWriteStr(s)
}

// TtyWriteStrSync writes a string to the TTY device with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
func (m *Modem) TtyWriteStrSync(s string) {
	m.Lock()
	defer m.Unlock()
	m.ttyWriteStr(s)
}

// Id returns the unique identifier of the modem instance.
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

// Cr returns the current carriage return sequence based on the modem's short form setting.
// Returns "\r" for short form, "\r\n" for verbose form.
// The modem lock must be held before calling this method.
func (m *Modem) Cr() string {
	m.checkLock()
	return m.cr()
}

// CrSync returns the current carriage return sequence with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
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
		// Write directly to TTY without error handling to avoid recursion during state transitions
		_, _ = m.tty.Write([]byte(m.cr() + retStr + m.cr()))
	}
}

// SetStatus changes the modem's operational status.
// The modem lock must be held before calling this method.
// Use SetStatusSync for automatic lock management.
func (m *Modem) SetStatus(status ModemStatus) {
	m.checkLock()
	m.setStatus(status)
}

// SetStatusSync changes the modem's operational status with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
func (m *Modem) SetStatusSync(status ModemStatus) {
	m.Lock()
	defer m.Unlock()
	m.setStatus(status)
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

		if m.conn != nil {
			m.conn.Close()
			m.conn = nil
		}

	case StatusConnected:
		if prevStatus != StatusDialing && prevStatus != StatusRinging && prevStatus != StatusConnectedCmd {
			panic(ErrInvalidStateTransition)
		}
		if prevStatus == StatusRinging {
			if m.answerChar != "" {
				// Cannot handle error by changing state inside setStatus to avoid recursion
				_, _ = m.conn.Write([]byte(m.answerChar[0:1]))
			}
			m.metrics.NumInConns++
		}
		if prevStatus == StatusDialing {
			m.metrics.NumOutConns++
		}
		m.metrics.NumConns++
		m.metrics.LastConnTime = time.Now()
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

// Status returns the current operational status of the modem.
// The modem lock must be held before calling this method.
// Use StatusSync for automatic lock management.
func (m *Modem) Status() ModemStatus {
	m.checkLock()
	return m.status()
}

// StatusSync returns the current operational status of the modem with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
func (m *Modem) StatusSync() ModemStatus {
	m.Lock()
	defer m.Unlock()
	return m.status()
}

func (m *Modem) close() {
	m.setStatus(StatusClosed)
}

// Close terminates the modem and closes all associated resources.
// The modem lock must be held before calling this method.
// Use CloseSync for automatic lock management.
func (m *Modem) Close() {
	m.checkLock()
	m.close()
}

// CloseSync terminates the modem and closes all associated resources with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
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
		m.metrics.ConnRxBytes += n
		m.Unlock()
		m.ttyWrite(buff[:n])
		m.Lock()
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

// IncomingCall simulates an incoming call by transitioning the modem to ringing state.
// The provided connection will be used for the call if answered.
// The modem lock must be held before calling this method.
// Use IncomingCallSync for automatic lock management.
func (m *Modem) IncomingCall(conn io.ReadWriteCloser) error {
	m.checkLock()
	return m.incomingCall(conn)
}

// IncomingCallSync simulates an incoming call with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
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
		if m.status() == StatusConnected || m.status() == StatusConnectedCmd {
			m.setStatus(StatusIdle)
			return RetCodeSilent
		}
	}
	return RetCodeOk
}

func (m *Modem) processAtCommand(cmd string) RetCode {
	if m.status() != StatusIdle && m.status() != StatusConnectedCmd && m.status() != StatusRinging {
		return RetCodeError
	}
	// Update LastAtCmdTime before processing hooks
	m.metrics.LastAtCmdTime = time.Now()
	// Call line hook if present
	if m.lineHook != nil {
		r := m.lineHook(m, cmd)
		if r != RetCodeSkip {
			return r
		}
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

// ProcessAtCommand processes an AT command string and returns the result code.
// The modem lock must be held before calling this method.
// Use ProcessAtCommandSync for automatic lock management.
func (m *Modem) ProcessAtCommand(cmd string) RetCode {
	m.checkLock()
	return m.processAtCommand(cmd)
}

// ProcessAtCommandSync processes an AT command string with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
func (m *Modem) ProcessAtCommandSync(cmd string) RetCode {
	m.Lock()
	defer m.Unlock()
	return m.processAtCommand(cmd)
}

// Metrics returns a copy of the current modem metrics and statistics.
// The modem lock must be held before calling this method.
// Use MetricsSync for automatic lock management.
func (m *Modem) Metrics() *Metrics {
	m.checkLock()
	copy := *m.metrics
	copy.Status = m.status()
	return &copy
}

// MetricsSync returns a copy of the current modem metrics and statistics with automatic lock management.
// This is a convenience method that acquires and releases the modem lock.
func (m *Modem) MetricsSync() *Metrics {
	m.Lock()
	defer m.Unlock()
	return m.Metrics()
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
		m.metrics.LastTtyRxTime = time.Now()
		m.metrics.TtyRxBytes += n
		if m.status() == StatusConnected { // online mode pass-through
			m.metrics.ConnTxBytes += n
			if m.conn != nil {
				if _, err := m.conn.Write(byteBuff); err != nil {
					// Connection write failed, disconnect
					m.setStatus(StatusIdle)
					continue
				}
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
				m.ttyWrite(byteBuff)
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
					m.ttyWrite(byteBuff)
				}
			}
		}
	}
	m.Unlock()
}

// NewModem creates a new modem instance with the specified configuration.
// The config parameter must not be nil and must contain at least Id and TTY fields.
// The modem will start in StatusIdle state and begin processing TTY input immediately.
//
// Returns ErrConfigRequired if config is nil or required fields are missing.
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
		lineHook:         config.LineHook,
		statusTransition: config.StatusTransition,
		tty:              config.TTY,
		connectStr:       config.ConnectStr,
		ringMax:          config.RingMax,
		answerChar:       config.AnswerChar,
		disablePreGuard:  config.DisablePreGuard,
		disablePostGuard: config.DisablePostGuard,
		echo:             true,
		sregs:            make(map[byte]byte),
		metrics:          &Metrics{},
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
