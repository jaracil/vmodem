package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/aymanbagabas/go-pty"
	"github.com/jaracil/nagle"
	vm "github.com/jaracil/vmodem"
	"github.com/jessevdk/go-flags"
	t "github.com/nayarsystems/iotrace"
	"go.bug.st/serial"
	"io"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Options struct {
	Verbose         []bool   `short:"v" long:"verbose" description:"Show verbose debug information"`
	ListenAddr      string   `short:"a" long:"addr" description:"Listen address" default:"0.0.0.0:2020"`
	DefaultPort     string   `short:"p" long:"port" description:"Default port for outgoing calls" default:"2020"`
	TtyPath         string   `short:"t" long:"tty" description:"path for TTYs creation" default:"/tmp/vmodem"`
	StartNum        int      `short:"s" long:"start" description:"Start number for TTYs" default:"0"`
	NumTTYs         int      `short:"n" long:"num" description:"Number of TTYs to create" default:"1"`
	RingMax         int      `short:"r" long:"ring" description:"Max number of rings before hangup" default:"10"`
	NoListen        bool     `short:"X" long:"nolisten" description:"Do not listen for incoming calls"`
	AnswerChar      string   `short:"S" long:"answer-char" description:"sends this character when the call is answered"`
	NagleSize       int      `short:"N" long:"nagle-size" description:"size of the nagle buffer 0 = disabled" default:"1024"`
	NagleTimeout    int      `short:"M" long:"nagle-timeout" description:"nagle timeout in milliseconds" default:"50"`
	GuardTime       int      `short:"G" long:"guard-time" description:"guard time in 50ms increments" default:"20"`
	DisablePreGuard bool     `short:"D" long:"disable-pre-guard" description:"disable pre-guard time for buggy implementations"`
	Command         []string `short:"C" long:"command" description:"Command hook. Format: regexp->response->result"`
	Translate       []string `short:"T" long:"translate" description:"Translate phone number to host. Format: regexp->format"`
	Attach          []string `short:"A" long:"attach" description:"Attach two TTY's. Format: tty1:tty2:speed,data_bits,parity,stop_bits"`
}

type Command struct {
	ReStr  string
	Output string
	Result vm.RetCode
	re     *regexp.Regexp
}

func NewCommand(reStr, format string, result vm.RetCode) (*Command, error) {
	re, err := regexp.Compile(reStr)
	if err != nil {
		return nil, err
	}
	return &Command{
		Output: format,
		ReStr:  reStr,
		Result: result,
		re:     re,
	}, nil
}

type NumToHost struct {
	Format string
	ReStr  string
	re     *regexp.Regexp
}

func NewNumToHost(reStr, format string) (*NumToHost, error) {
	re, err := regexp.Compile(reStr)
	if err != nil {
		return nil, err
	}
	return &NumToHost{
		Format: format,
		ReStr:  reStr,
		re:     re,
	}, nil
}

func (n *NumToHost) Match(num string) string {
	m := n.re.FindStringSubmatch(num)
	if len(m) == 0 {
		return ""
	}
	var as []interface{}
	for _, v := range m[1:] {
		as = append(as, v)
	}
	return fmt.Sprintf(n.Format, as...)
}

var (
	ctx        context.Context
	cancel     context.CancelFunc
	options    Options
	modems     []*vm.Modem
	attached1  []serial.Port
	attached2  []serial.Port
	listener   net.Listener
	numToHosts []*NumToHost
	commands   []*Command
)

func findHost(num string) string {
	for _, n := range numToHosts {
		host := n.Match(num)
		if host != "" {
			return host
		}
	}
	return ""
}

func outGoingCall(m *vm.Modem, number string) (io.ReadWriteCloser, error) {
	host := findHost(number)
	if host != "" {
		if !strings.Contains(host, ":") {
			host = fmt.Sprintf("%s:%s", host, options.DefaultPort)
		}
		if len(options.Verbose) > 0 {
			fmt.Printf("%s: Dialing %s -> %s\n", m.Id(), number, host)
		}
		conn, err := net.Dial("tcp", host)
		if err != nil {
			return nil, err
		}
		var connWrapp io.ReadWriteCloser
		if options.NagleSize > 0 {
			connWrapp = nagle.NewNagleWrapper(conn, options.NagleSize, time.Millisecond*time.Duration(options.NagleTimeout))
		} else {
			connWrapp = conn
		}
		return connWrapp, nil
	}
	if len(options.Verbose) > 0 {
		fmt.Printf("%s: Dialing %s -> no host found\n", m.Id(), number)
	}
	return nil, vm.ErrNoCarrier
}

func commandHook(m *vm.Modem, cmdChar string, cmdNum string, cmdAssign bool, cmdQuery bool, cmdAssignVal string) vm.RetCode {
	if len(options.Verbose) > 1 {
		fmt.Printf("%s: Command with params: cmd:%s num:%s assign:%v query:%v val:%s\n", m.Id(), cmdChar, cmdNum, cmdAssign, cmdQuery, cmdAssignVal)
	}
	cmd := fmt.Sprintf("%s%s", cmdChar, cmdNum)
	if cmdAssign {
		cmd += "="
	}
	if cmdQuery {
		cmd += "?"
	}
	if cmdAssignVal != "" {
		cmd += cmdAssignVal
	}
	for _, c := range commands {
		if c.re.MatchString(cmd) {
			if c.Output != "" {
				m.TtyWriteStr(fmt.Sprintf("\r\n%s\r\n", c.Output))
			}
			return c.Result
		}
	}
	return vm.RetCodeSkip
}

func statusTransition(m *vm.Modem, oldStatus vm.ModemStatus, newStatus vm.ModemStatus) {
	if len(options.Verbose) > 0 {
		fmt.Printf("%s: Status transition %v -> %v\n", m.Id(), oldStatus, newStatus)
	}
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

func cleanAttached() {
	for _, port := range attached1 {
		port.Close()
	}
	for _, port := range attached2 {
		port.Close()
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
			cancel()
			break
		}
		var connWrapp io.ReadWriteCloser
		if options.NagleSize > 0 {
			connWrapp = nagle.NewNagleWrapper(conn, options.NagleSize, time.Millisecond*time.Duration(options.NagleTimeout))
		} else {
			connWrapp = conn
		}
		assigned := false
		// Find a free modem
		for i := 0; i < options.NumTTYs; i++ {
			if err := modems[i].IncomingCallSync(connWrapp); err == nil {
				assigned = true
				break
			}
		}
		if !assigned {
			connWrapp.Close()
			fmt.Fprintf(os.Stderr, "No free modems for incomming call\n")
		}
	}
}

func linkPorts(port1, port2 serial.Port) {
	go func() {
		io.Copy(port1, port2)
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Broken tty attach\n")
			cancel()
		}

	}()
	go func() {
		io.Copy(port2, port1)
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Broken tty attach\n")
			cancel()
		}
	}()
}

func attachTTY(cfgStr string) error {

	params := strings.Split(cfgStr, ":")
	if len(params) < 2 {
		return fmt.Errorf("invalid attach string")
	}

	serialPort1 := params[0]
	serialPort2 := params[1]
	serialParams := []string{}
	if len(params) > 2 {
		serialParams = strings.Split(params[2], ",")
	}
	serialSpeed := 9600
	serialDataBits := 8
	serialParity := serial.NoParity
	serialStopBits := serial.OneStopBit
	var err error

	if len(serialParams) >= 1 {
		serialSpeed, err = strconv.Atoi(serialParams[0])
		if err != nil {
			return fmt.Errorf("invalid speed")
		}
	}
	if len(serialParams) >= 2 {
		serialDataBits, err = strconv.Atoi(serialParams[1])
		if err != nil {
			return fmt.Errorf("invalid data bits")
		}
	}
	if len(serialParams) >= 3 {
		switch strings.ToUpper(serialParams[2]) {
		case "N":
			serialParity = serial.NoParity
		case "E":
			serialParity = serial.EvenParity
		case "O":
			serialParity = serial.OddParity
		default:
			return fmt.Errorf("invalid parity")
		}
	}
	if len(serialParams) >= 4 {
		switch serialParams[3] {
		case "1":
			serialStopBits = serial.OneStopBit
		case "2":
			serialStopBits = serial.TwoStopBits
		default:
			return fmt.Errorf("invalid stop bits")
		}
	}

	port1, err := serial.Open(serialPort1, &serial.Mode{
		BaudRate: serialSpeed,
		DataBits: serialDataBits,
		Parity:   serialParity,
		StopBits: serialStopBits,
	})
	if err != nil {
		return fmt.Errorf("error opening external serial port: %v", err)
	}
	port2, err := serial.Open(serialPort2, &serial.Mode{
		BaudRate: serialSpeed,
		DataBits: serialDataBits,
		Parity:   serialParity,
		StopBits: serialStopBits,
	})
	if err != nil {
		return fmt.Errorf("error opening local serial port: %v", err)
	}
	attached1 = append(attached1, port1)
	attached2 = append(attached2, port2)
	go linkPorts(port1, port2)
	return nil
}

func phoneTranslations() {
	defaultNumToHost, err := NewNumToHost("\\*(\\d{1,3})\\*(\\d{1,3})\\*(\\d{1,3})\\*(\\d{1,3})\\*(\\d{1,5})?", "%[1]s.%[2]s.%[3]s.%[4]s:%[5]s")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating default NumToHost: %v\n", err)
		os.Exit(1)
	}
	numToHosts = append(numToHosts, defaultNumToHost)
	defaultNumToHost, err = NewNumToHost("\\*(\\d{1,3})\\*(\\d{1,3})\\*(\\d{1,3})\\*(\\d{1,3})", "%[1]s.%[2]s.%[3]s.%[4]s")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating default NumToHost: %v\n", err)
		os.Exit(1)
	}
	numToHosts = append(numToHosts, defaultNumToHost)
	defaultNumToHost, err = NewNumToHost("(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3}):(\\d{1,5})?", "%[1]s.%[2]s.%[3]s.%[4]s:%[5]s")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating default NumToHost: %v\n", err)
		os.Exit(1)
	}
	numToHosts = append(numToHosts, defaultNumToHost)
	defaultNumToHost, err = NewNumToHost("(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})", "%[1]s.%[2]s.%[3]s.%[4]s")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating default NumToHost: %v\n", err)
		os.Exit(1)
	}
	numToHosts = append(numToHosts, defaultNumToHost)
	for _, t := range options.Translate {
		parts := strings.Split(t, "->")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid translation: %s\n", t)
			os.Exit(1)
		}
		numToHost, err := NewNumToHost(parts[0], parts[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating NumToHost: %v\n", err)
			os.Exit(1)
		}
		numToHosts = append(numToHosts, numToHost)
	}
}

func customCommands() {
	for _, c := range options.Command {
		parts := strings.Split(c, "->")
		if len(parts) != 3 {
			fmt.Fprintf(os.Stderr, "Invalid command: %s\n", c)
			os.Exit(1)
		}
		cmdRet := vm.CmdReturnFromString(parts[2])
		if cmdRet == vm.RetCodeUnknown {
			fmt.Fprintf(os.Stderr, "Invalid command return: %s\n", parts[2])
			os.Exit(1)
		}
		cmd, err := NewCommand(parts[0], parts[1], cmdRet)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating command: %v\n", err)
			os.Exit(1)
		}
		commands = append(commands, cmd)
	}
}

var tini = time.Now()

type bytesHookFunc func([]byte)

func newModemTraceHook(prefix string) bytesHookFunc {
	return func(data []byte) {
		fmt.Printf("(%d) %s:\n%s\n", time.Since(tini).Milliseconds(), prefix, hex.Dump(data))
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

	phoneTranslations()
	customCommands()

	for i := 0; i < options.NumTTYs; i++ {
		tty, err := pty.New()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tty: %v\n", err)
			os.Exit(1)
		}

		id := fmt.Sprintf("tty%d", options.StartNum+i)
		var rwc io.ReadWriteCloser
		if len(options.Verbose) > 2 {
			rwc = t.NewRWCTracer(tty, 16, time.Millisecond*time.Duration(options.NagleTimeout),
				newModemTraceHook(fmt.Sprintf("%s-w", id)),
				newModemTraceHook(fmt.Sprintf("%s-r", id)),
			)
		} else {
			rwc = tty
		}

		m, err := vm.NewModem(&vm.ModemConfig{
			Id:               id,
			OutgoingCall:     outGoingCall,
			CommandHook:      commandHook,
			StatusTransition: statusTransition,
			TTY:              rwc,
			RingMax:          options.RingMax,
			AnswerChar:       options.AnswerChar,
			GuardTime:        options.GuardTime,
			DisablePreGuard:  options.DisablePreGuard,
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
		if len(options.Verbose) > 0 {
			fmt.Printf("%s: Created and listen on %s/tty%d\n", m.Id(), options.TtyPath, options.StartNum+i)
		}
	}

	for _, attachStr := range options.Attach {
		err := attachTTY(attachStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error attaching TTY: %v\n", err)
			os.Exit(1)
		}
	}

	if !options.NoListen {
		go listenTask()
	}
	fmt.Println("Vmodem started, press Ctrl+C to exit")
	<-ctx.Done()
	if listener != nil {
		listener.Close()
	}
	cleanTTYs()
	cleanAttached()
	cleanModems()
}
