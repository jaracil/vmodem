# VModem Server - Reference Implementation

VModem Server is a complete virtual Hayes-compatible modem server built using the [VModem library](../../). It creates virtual TTY devices and provides modem functionality for legacy systems over modern TCP/IP networks.

## Features

- **Virtual TTY Creation**: Creates Unix pseudo-terminals that appear as real modem devices
- **TCP Server**: Accepts incoming connections and routes them to available modems
- **Phone Number Translation**: Flexible pattern matching to convert phone numbers to IP addresses
- **Serial Port Integration**: Can bridge virtual modems with real serial ports
- **HTTP Metrics Endpoint**: Real-time monitoring and statistics
- **Custom AT Commands**: Extensible command processing via hooks
- **Watchdog Timer**: Automatic connection timeout detection
- **Production Ready**: Comprehensive logging and error handling

## Installation

```bash
go install github.com/jaracil/vmodem/cmd/vmodem@latest
```

Or build from source:

```bash
git clone https://github.com/jaracil/vmodem
cd vmodem
go build -o vmodem cmd/vmodem/*.go
```

## Quick Start

Start a basic virtual modem server:

```bash
./vmodem -n 2 -a 0.0.0.0:2020
```

This creates 2 virtual modems listening on port 2020, with TTY devices at:
- `/tmp/vmodem/tty0`
- `/tmp/vmodem/tty1`

## Usage

### Command Line Options

```bash
./vmodem [OPTIONS]
```

**Core Options:**
- `-n, --num <count>`: Number of TTYs to create (default: 1)
- `-a, --addr <address>`: Listen address (default: 0.0.0.0:2020)
- `-t, --tty <path>`: Path for TTYs creation (default: /tmp/vmodem)
- `-s, --start <num>`: Start number for TTYs (default: 0)
- `-v, --verbose`: Show verbose debug information (use multiple times for more detail)

**Modem Behavior:**
- `-r, --ring <count>`: Max number of rings before hangup (default: 10)
- `-S, --answer-char <char>`: Sends this character when the call is answered
- `-G, --guard-time <time>`: Guard time in 50ms increments (default: 20)
- `-D, --disable-pre-guard`: Disable pre-guard time for buggy implementations
- `-P, --disable-post-guard`: Disable post-guard time for buggy implementations

**Network Options:**
- `-p, --port <port>`: Default port for outgoing calls (default: 2020)
- `-X, --nolisten`: Do not listen for incoming calls
- `-N, --nagle-size <bytes>`: Size of the nagle buffer, 0 = disabled (default: 1024)
- `-M, --nagle-timeout <ms>`: Nagle timeout in milliseconds (default: 50)

**Advanced Features:**
- `-w, --watchdog <seconds>`: Connection timeout in seconds (0 = disabled, default: 0)
- `-m, --metrics <address>`: Enable metrics http server. Format: host:port
- `-C, --command <pattern>`: Command hook. Format: regexp->response->result
- `-L, --line <pattern>`: Line hook. Format: regexp->response->result
- `-T, --translate <pattern>`: Translate phone number to host. Format: regexp->format
- `-A, --attach <config>`: Attach two TTY's. Format: tty1:tty2:speed,data_bits,parity,stop_bits
- `-I, --init <command>`: AT commands to initialize each modem (without AT prefix)

### Phone Number Translation

Configure how phone numbers are translated to network addresses:

```bash
# Custom translation patterns
./vmodem -T "^555(\\d{4})$->192.168.1.100:\\1" -T "^800.*->example.com:2020"

# Default patterns (built-in):
# *192*168*1*100*2020 -> 192.168.1.100:2020
# *192*168*1*100      -> 192.168.1.100:2020
# 192.168.1.100:2020  -> 192.168.1.100:2020
# 192.168.1.100       -> 192.168.1.100:2020
```

### Custom AT Commands

Add custom AT command responses using command hooks (match individual commands):

```bash
# Custom command hooks: pattern->response->result
./vmodem -C "^I0$->VModem Server v1.0->OK" -C "^I1$->Virtual Modem->OK"
```

Or use line hooks (match complete command lines before parsing):

```bash
# Line hooks intercept entire command lines
./vmodem -L "^CUSTOM.*->Custom command executed->OK"
```

**Difference between Command and Line hooks:**
- Command hooks (`-C`) match individual parsed AT commands
- Line hooks (`-L`) match the entire command line before parsing, useful for custom syntax

### Modem Initialization

Initialize modems with AT commands on startup (before TTY is exposed):

```bash
# Initialize with echo off and verbose mode
./vmodem -I "e0" -I "v1"

# Multiple commands can be chained in one initialization
./vmodem -I "e0v1q0"

# Set S-registers on startup
./vmodem -I "s0=2"  # Auto-answer after 2 rings
```

This is useful for setting default modem behavior before applications connect.

### Serial Port Integration

Bridge virtual modems with real serial ports:

```bash
# Attach serial ports: port1:port2:speed,data,parity,stop
./vmodem -A "/dev/ttyUSB0:/tmp/vmodem/tty0:9600,8,N,1"
```

### Metrics and Monitoring

Enable HTTP metrics endpoint:

```bash
./vmodem -m localhost:8080
```

Access metrics at:
- `http://localhost:8080/` - JSON metrics for all modems
- `http://localhost:8080/proc` - Server uptime information

**Metrics JSON Response includes:**
- `status` - Current modem state (Detached, Idle, Dialing, Connected, ConnectedCmd, Ringing, Closed)
- `modemId` - Modem identifier
- Byte counters: `ttyRxBytes`, `ttyTxBytes`, `connRxBytes`, `connTxBytes`
- Connection counts: `numConns`, `numInConns`, `numOutConns`
- Timestamps: `lastTtyRxMs`, `lastTtyTxMs`, `lastAtCmdMs`, `lastConnMs`

## Examples

### Basic Virtual Modem

```bash
# Create 1 modem, listen on port 2020
./vmodem
```

Connect to the modem:
```bash
# In another terminal
cu -l /tmp/vmodem/tty0
AT        # Should respond with OK
ATDT*192*168*1*100*80  # Dial 192.168.1.100:80
```

### Multiple Modems with Custom Settings

```bash
# 4 modems, custom path, verbose logging, metrics enabled
./vmodem -n 4 -t /var/lib/vmodem -v -m localhost:9090 -r 5
```

### Production Setup

```bash
# Production configuration with monitoring and custom translations
./vmodem \
  -n 8 \
  -a 0.0.0.0:2020 \
  -t /var/lib/vmodem \
  -m 0.0.0.0:8080 \
  -w 300 \
  -I "e0v1" \
  -T "^(\\d{3})(\\d{3})(\\d{4})$->pbx.company.com:\\1\\2\\3" \
  -C "^I0$->CompanyModem v2.1->OK" \
  -L "^PING$->PONG->OK" \
  --nagle-size 2048 \
  --nagle-timeout 100
```

### Serial Port Bridge

```bash
# Bridge physical modem to virtual TTY
./vmodem -A "/dev/ttyS0:/tmp/vmodem/tty0:57600,8,N,1" -X
```

## Architecture

The VModem server demonstrates several key patterns:

### TTY Management
- Creates Unix pseudo-terminals using `github.com/creack/pty`
- Manages symlinks for easy access
- Handles cleanup on shutdown

### Network Server
- TCP listener for incoming connections
- Load balancing across available modems
- Connection routing and management

### Phone Number Translation
- Regex-based pattern matching
- Multiple translation rules
- Default built-in patterns

### Monitoring
- Real-time metrics collection
- HTTP endpoint for monitoring tools
- Uptime and connection statistics

## Configuration Files

The server can be configured via command line only. For complex setups, consider using a wrapper script or configuration management tool.

## Dependencies

- `github.com/jaracil/vmodem`: Core modem library
- `github.com/creack/pty`: PTY creation and management
- `github.com/jaracil/nagle`: Network optimization
- `github.com/jessevdk/go-flags`: Command-line parsing
- `github.com/nayarsystems/iotrace`: I/O tracing for debugging
- `go.bug.st/serial`: Serial port communication

## Troubleshooting

### Common Issues

**TTY Permission Denied:**
```bash
# Ensure proper permissions
sudo chown $USER /tmp/vmodem/tty*
sudo chmod 666 /tmp/vmodem/tty*
```

**Port Already in Use:**
```bash
# Check what's using the port
netstat -tulpn | grep :2020
# Use a different port
./vmodem -a 0.0.0.0:2021
```

**Connection Timeouts:**
```bash
# Enable watchdog for automatic cleanup
./vmodem -w 60  # 60 second timeout
```

### Debug Logging

Use verbose flags for debugging:
- `-v`: Basic operation logging
- `-vv`: Detailed state transitions
- `-vvv`: Full I/O tracing with hex dumps

## Performance

The server is designed for production use and can handle:
- Multiple concurrent connections per modem
- High throughput data transfer
- Long-running stable operation
- Automatic resource cleanup

For optimal performance:
- Use appropriate Nagle buffering settings
- Enable watchdog timers for stuck connections
- Monitor metrics endpoint for bottlenecks

## License

This project is licensed under the MIT License.

## Support

For issues and questions, please use the GitHub issue tracker at the main repository.