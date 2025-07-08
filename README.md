# VModem - Virtual Hayes-Compatible Modem Library

VModem is a Go library that implements a virtual Hayes-compatible modem simulator over TCP/IP networks. It provides complete AT command processing, state management, and network connectivity for legacy systems that need to communicate over modern networks.

## Features

- **Complete Hayes AT Command Set**: Supports standard AT commands including dial, answer, hangup, configuration, and S-registers
- **State Machine Management**: Robust modem state transitions (Idle, Dialing, Connected, Ringing, etc.)
- **TCP/IP Transport**: Routes modem calls over modern TCP/IP networks
- **Metrics and Monitoring**: Built-in statistics tracking and performance monitoring
- **Concurrent Operations**: Thread-safe design with proper goroutine management
- **Configurable Behavior**: Extensive configuration options for timeouts, guards, and responses
- **Zero External Dependencies**: Uses only Go standard library

## Installation

```bash
go get github.com/jaracil/vmodem
```

## Quick Start

```go
package main

import (
    "log"
    "github.com/jaracil/vmodem"
)

func main() {
    // Create a mock TTY for this example
    tty := &MockTTY{}
    
    config := &vmodem.ModemConfig{
        Id:         "my-modem",
        TTY:        tty,
        ConnectStr: "CONNECT 9600",
        RingMax:    10,
    }
    
    modem, err := vmodem.NewModem(config)
    if err != nil {
        log.Fatal(err)
    }
    defer modem.CloseSync()
    
    // Process AT commands
    result := modem.ProcessAtCommandSync("ATE1")
    log.Printf("Command result: %v", result)
}
```

## Architecture

### Core Components

- **Modem Engine**: Central state machine handling AT commands and modem behavior
- **TTY Interface**: Virtual terminal integration for legacy compatibility  
- **Network Layer**: TCP/IP transport for modern connectivity
- **Command Parser**: Full AT command syntax support with chaining and validation
- **Metrics System**: Runtime statistics and performance monitoring

### State Machine

The modem implements a strict state machine with the following states:

```
Idle → Dialing → Connected ⇄ ConnectedCmd
  ↓       ↓
Ringing → Connected
  ↓
Closed (terminal state)
```

### Supported AT Commands

- **Basic Commands**: `E` (echo), `V` (verbose), `Q` (quiet), `H` (hangup)
- **Connection**: `D` (dial), `A` (answer), `O` (online)
- **Configuration**: `S` registers, `&F` (factory reset), `Z` (reset)
- **Advanced**: Command chaining, `A/` (repeat last command)

## Configuration

### ModemConfig Options

```go
type ModemConfig struct {
    Id               string                    // Modem identifier
    TTY              io.ReadWriteCloser       // TTY interface
    OutgoingCall     OutgoingCallType         // Dial-out handler
    CommandHook      CommandHookType          // Custom AT command hook
    StatusTransition StatusTransitionType     // State change notifications
    ConnectStr       string                   // Connect response string
    RingMax          int                      // Maximum rings before timeout
    AnswerChar       string                   // Answer character to send/expect
    GuardTime        int                      // Escape sequence guard time
    DisablePreGuard  bool                     // Disable pre-guard time
    DisablePostGuard bool                     // Disable post-guard time
}
```

### Hook Functions

Customize modem behavior with hook functions:

```go
// Custom AT command processing
func commandHook(m *vmodem.Modem, cmdChar string, cmdNum string, 
                 cmdAssign bool, cmdQuery bool, cmdAssignVal string) vmodem.RetCode {
    if cmdChar == "I" && cmdNum == "0" {
        m.TtyWriteStr("VModem v1.0")
        return vmodem.RetCodeOk
    }
    return vmodem.RetCodeSkip // Let default processing handle it
}

// Outgoing call handler
func outgoingCall(m *vmodem.Modem, number string) (io.ReadWriteCloser, error) {
    return net.Dial("tcp", translateNumber(number))
}
```

## Reference Implementation

See [`cmd/vmodem`](./cmd/vmodem) for a complete reference implementation that demonstrates:

- Virtual TTY creation and management
- TCP server for incoming connections
- Phone number translation patterns
- Command-line configuration
- Metrics HTTP endpoint
- Serial port integration
- Production-ready deployment

The reference implementation serves as both a working virtual modem server and a comprehensive example of how to use this library.

## Testing

The library includes comprehensive unit tests covering:

- Pure function testing
- State machine transitions
- AT command processing (both direct and TTY flow)
- Concurrent operations
- Error handling

Run tests with:

```bash
go test ./...
```

## Metrics

The library provides detailed runtime metrics:

```go
type Metrics struct {
    Status        ModemStatus // Current modem state
    TtyTxBytes    int        // Bytes transmitted to TTY
    TtyRxBytes    int        // Bytes received from TTY
    ConnTxBytes   int        // Bytes transmitted to network
    ConnRxBytes   int        // Bytes received from network
    NumConns      int        // Total connections
    NumInConns    int        // Incoming connections
    NumOutConns   int        // Outgoing connections
    LastTtyTxTime time.Time  // Last TTY transmission
    LastTtyRxTime time.Time  // Last TTY reception
    LastAtCmdTime time.Time  // Last AT command
    LastConnTime  time.Time  // Last connection time
}
```

## Error Handling

The library defines specific error types:

- `ErrConfigRequired`: Invalid or missing configuration
- `ErrModemBusy`: Modem unavailable for new operations
- `ErrInvalidStateTransition`: Illegal state change attempted
- `ErrNoCarrier`: Connection failed or lost

## Thread Safety

All public methods are thread-safe and provide both synchronous and asynchronous variants:

- `Status()` / `StatusSync()`: Get modem state
- `SetStatus()` / `SetStatusSync()`: Change modem state  
- `ProcessAtCommand()` / `ProcessAtCommandSync()`: Execute AT commands
- `IncomingCall()` / `IncomingCallSync()`: Handle incoming connections

## API Documentation

Complete API documentation is available at [pkg.go.dev](https://pkg.go.dev/github.com/jaracil/vmodem).

## Dependencies

- **Go Standard Library Only**: No external dependencies required

## License

This project is licensed under the MIT License.

## Contributing

Contributions are welcome! Please ensure all tests pass and follow the existing code style.

## Support

For issues and questions, please use the GitHub issue tracker.