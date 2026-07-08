# go-nat-listener

A NAT traversal library for Go that provides standard network interfaces with automatic port mapping and renewal. This library enables applications running behind SOHO routers to accept incoming connections by automatically configuring port forwarding through UPnP and NAT-PMP protocols.

## Features

- **Standard Go Network Interfaces**: Drop-in replacements for `net.Listen` and `net.ListenPacket`
- **Automatic NAT Traversal**: Supports UPnP and NAT-PMP protocols with automatic fallback
- **Opt-in TCP Punching**: TCP listeners can combine inner router mapping with an outbound binding and expose the public endpoint observed by TCP STUN
- **Port Renewal**: Automatically renews port mappings to maintain connectivity
- **TCP and UDP Support**: Works with both TCP listeners and UDP packet connections
- **External Address Discovery**: Provides access to both internal and external network addresses
- **Structured Logging**: Comprehensive structured logging via `github.com/go-i2p/logger`

## Installation

```bash
go get github.com/go-i2p/go-nat-listener
```

## Requirements

- Go 1.24.5 or later
- Router with UPnP or NAT-PMP support, plus a configured TCP punch keepalive/STUN path when using TCP punching
- Network environment allowing NAT traversal protocols or outbound TCP keepalive connections

## Quick Start

> **Note:** This library only supports IPv4 networks. See [Limitations](#limitations) for details.

### TCP Listener

```go
package main

import (
    "fmt"
    "log"
    "net"
    
    "github.com/go-i2p/go-nat-listener"
)

func main() {
    // Create a NAT-traversing TCP listener on port 8080
    listener, err := nattraversal.Listen(8080)
    if err != nil {
        log.Fatal("Failed to create listener:", err)
    }
    defer listener.Close()
    
    fmt.Printf("Listening on %s (external: %s)\n", 
        listener.Addr().(*nattraversal.NATAddr).InternalAddr(),
        listener.Addr().String())
    
    for {
        conn, err := listener.Accept()
        if err != nil {
            log.Printf("Accept error: %v", err)
            continue
        }
        
        go handleConnection(conn)
    }
}

func handleConnection(conn net.Conn) {
    defer conn.Close()
    // Handle the connection...
}
```

### UDP Packet Listener

```go
package main

import (
    "fmt"
    "log"
    "net"
    
    "github.com/go-i2p/go-nat-listener"
)

func main() {
    // Create a NAT-traversing UDP listener on port 9090
    listener, err := nattraversal.ListenPacket(9090)
    if err != nil {
        log.Fatal("Failed to create packet listener:", err)
    }
    defer listener.Close()
    
    fmt.Printf("UDP listening on %s (external: %s)\n",
        listener.Addr().(*nattraversal.NATAddr).InternalAddr(),
        listener.Addr().String())
    
    // Get the underlying PacketConn for reading/writing
    conn := listener.PacketConn()
    
    buffer := make([]byte, 1024)
    for {
        n, addr, err := conn.ReadFrom(buffer)
        if err != nil {
            log.Printf("Read error: %v", err)
            continue
        }
        
        fmt.Printf("Received %d bytes from %s: %s\n", n, addr, string(buffer[:n]))
    }
}
```

### With Context (Timeout/Cancellation)

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"
    
    "github.com/go-i2p/go-nat-listener"
)

func main() {
    // Create a context with a 10-second timeout for NAT discovery
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    // Create a NAT-traversing TCP listener with timeout
    listener, err := nattraversal.ListenContext(ctx, 8080)
    if err != nil {
        log.Fatal("Failed to create listener:", err)
    }
    defer listener.Close()
    
    fmt.Printf("Listening on external port: %d\n", listener.ExternalPort())
    
    // Accept connections normally...
}
```

## API Reference

### Core Functions

#### `Listen(port int) (*NATListener, error)`
Creates a TCP listener with NAT traversal on the specified port. Returns a `NATListener` that implements the standard `net.Listener` interface. This is a convenience wrapper around `ListenContext` using `context.Background()`.

#### `ListenContext(ctx context.Context, port int) (*NATListener, error)`
Creates a TCP listener with NAT traversal on the specified port, with context support for cancellation and timeouts. The context can be used to cancel the discovery and mapping operations. Once the listener is created, the context is no longer used - use `Close()` to stop the listener.

#### `ListenPacket(port int) (*NATPacketListener, error)`
Creates a UDP packet listener with NAT traversal on the specified port. Returns a `NATPacketListener` for UDP communication. This is a convenience wrapper around `ListenPacketContext` using `context.Background()`.

#### `ListenPacketContext(ctx context.Context, port int) (*NATPacketListener, error)`
Creates a UDP packet listener with NAT traversal on the specified port, with context support for cancellation and timeouts. The context can be used to cancel the discovery and mapping operations. Once the listener is created, the context is no longer used - use `Close()` to stop the listener.

#### `ListenWithFallback(port int) (*NATListener, error)`
Creates a TCP listener that attempts NAT traversal first, but falls back to a standard `net.Listener` if NAT traversal fails (e.g., when UPnP and NAT-PMP are both unavailable). This is useful when you want your application to work even in environments without NAT support. Use `IsFallback()` to check if fallback mode is active.

#### `ListenWithFallbackContext(ctx context.Context, port int) (*NATListener, error)`
Creates a TCP listener with fallback support and context for cancellation/timeouts.

#### `ListenPacketWithFallback(port int) (*NATPacketListener, error)`
Creates a UDP packet listener that attempts NAT traversal first, but falls back to a standard `net.PacketConn` if NAT traversal fails. Use `IsFallback()` to check if fallback mode is active.

#### `ListenPacketWithFallbackContext(ctx context.Context, port int) (*NATPacketListener, error)`
Creates a UDP packet listener with fallback support and context for cancellation/timeouts.

### Types

#### `NATListener`
Implements `net.Listener` with automatic NAT traversal support:
- `Accept() (net.Conn, error)` - Accepts incoming connections
- `Close() error` - Closes the listener and stops port renewal
- `Addr() net.Addr` - Returns the NAT-aware address
- `ExternalPort() int` - Returns the external port number assigned by the NAT device (same as internal port in fallback mode)
- `IsFallback() bool` - Returns true if NAT traversal failed and the listener is using a standard `net.Listener` without NAT hole-punching

#### `NATPacketListener`
Provides UDP packet listening with NAT traversal:
- `Accept() (net.PacketConn, error)` - Returns the underlying packet connection. Unlike TCP's `Accept()` which blocks for new connections, this returns the same cached `NATPacketConn` instance each time (UDP is connectionless). Prefer using `PacketConn()` for direct access.
- `Close() error` - Closes the listener and stops port renewal
- `Addr() net.Addr` - Returns the NAT-aware address
- `PacketConn() net.PacketConn` - Direct access to the packet connection
- `ExternalPort() int` - Returns the external port number assigned by the NAT device (same as internal port in fallback mode)
- `IsFallback() bool` - Returns true if NAT traversal failed and the listener is using a standard `net.PacketConn` without NAT hole-punching

#### `NATAddr`
Network address with NAT traversal information:
- `Network() string` - Returns the network type (tcp/udp)
- `String() string` - Returns the external address (same as `ExternalAddr()`)
- `InternalAddr() string` - Returns the internal network address
- `ExternalAddr() string` - Returns the external network address

> **Note:** `String()` returns the external address to satisfy the `net.Addr` interface, making `NATAddr` work seamlessly with code expecting standard network addresses.

#### `NATConn`
Wraps `net.Conn` with NAT-aware addressing:
- `LocalAddr() net.Addr` - Returns NAT-aware local address
- `RemoteAddr() net.Addr` - Returns remote address
- Embeds `net.Conn` for all standard connection operations

## How It Works

1. **TCP Punching (optional)**: If enabled through environment variables, TCP listeners first map the same local TCP port on the inner router using UPnP or NAT-PMP, then create a long-lived outbound TCP connection from that port and query TCP STUN from that same local port. This exposes the upstream NAT-assigned public endpoint, such as a CGNAT random port.
2. **Port Mapping**: If TCP punching is disabled or unavailable, the library attempts to create a port mapping on your router using UPnP first, then falls back to NAT-PMP
3. **External IP Discovery**: Retrieves the external IP address reported by the selected traversal method
4. **Address Management**: Provides both internal (LAN) and external (WAN) addresses
5. **Automatic Renewal**: Continuously renews port mappings to prevent expiration
6. **Standard Interfaces**: Exposes familiar Go network interfaces for easy integration

## Supported Protocols

- **TCP Punching**: Optional TCP-only mode for networks where UPnP or NAT-PMP reaches the inner router and the real public endpoint is assigned by an upstream NAT
- **UPnP (Universal Plug and Play)**: Primary protocol for automatic port forwarding
- **NAT-PMP (NAT Port Mapping Protocol)**: Fallback protocol for routers that don't support UPnP

## TCP Punching

TCP punching is disabled by default. Enable it only when you have an inner router mapping protocol (UPnP or NAT-PMP), a TCP keepalive target, and a TCP STUN server. The inner mapping, keepalive connection, and STUN probe all use the listener's local port, so the returned `NATAddr` can use the upstream NAT-assigned public address and random port.

### 中文实战：从零开始打 CGNAT TCP 洞

适用场景：

- 机器在路由器 LAN 下面，例如 `192.168.10.204`。
- 路由器 WAN 口不是公网，例如 `172.19.x.x`、`100.64.x.x`、`10.x.x.x`。
- 真正公网 IP 在运营商 NAT 外面，例如 `223.73.225.207`。
- 路由器支持 UPnP 或 NAT-PMP，并且本机能访问路由器的 UPnP。
- 外网 TCP 入站端口不是你本地监听端口，而是运营商 NAT 分配的随机端口。

穿透链路：

1. 本机监听固定端口，比如 `19880/TCP`。
2. 库先用 UPnP/NAT-PMP 在内层路由器上做映射：`192.168.10.204:19880 -> 路由器WAN:19880`。
3. 库再从同一个本地端口 `19880` 主动连 TCP 保活服务器，例如 `qq.com:80`。
4. 库用同一个本地端口 `19880` 连 TCP STUN，拿到运营商 NAT 看到的公网地址，例如 `223.73.225.207:59768`。
5. 对外公布和测试的是 `223.73.225.207:59768`，不是 `223.73.225.207:19880`。

路由器里看到类似这一行是正常的：

```text
DESKTOP-TAFFUCS.lan  192.168.10.204  19880  19880  TCP  nattraversal
```

这只说明“内层路由器 -> 本机”这段通了；真正给外部访问的是 TCP STUN 返回的公网随机端口。

最小代码：

```go
package main

import (
    "fmt"
    "io"
    "log"
    "net"

    nattraversal "github.com/go-i2p/go-nat-listener"
)

func main() {
    listener, err := nattraversal.Listen(19880)
    if err != nil {
        log.Fatal(err)
    }
    defer listener.Close()

    fmt.Println("external:", listener.Addr())

    for {
        conn, err := listener.Accept()
        if err != nil {
            return
        }
        go func(conn net.Conn) {
            defer conn.Close()
            _, _ = io.WriteString(conn, "ok\n")
        }(conn)
    }
}
```

Windows PowerShell 启动：

```powershell
$env:NATLISTENER_TCP_PUNCH_ENABLE = 'true'
$env:NATLISTENER_TCP_PUNCH_KEEPALIVE_ADDR = 'qq.com:80'
$env:NATLISTENER_TCP_PUNCH_STUN_ADDR = 'stun.nextcloud.com:3478'
$env:NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL = '1s'
$env:NATLISTENER_TCP_PUNCH_RETRY_INTERVAL = '1s'
$env:NATLISTENER_TCP_PUNCH_DIAL_TIMEOUT = '5s'
go run .
```

Linux/macOS 启动：

```bash
NATLISTENER_TCP_PUNCH_ENABLE=true \
NATLISTENER_TCP_PUNCH_KEEPALIVE_ADDR=qq.com:80 \
NATLISTENER_TCP_PUNCH_STUN_ADDR=stun.nextcloud.com:3478 \
NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL=1s \
NATLISTENER_TCP_PUNCH_RETRY_INTERVAL=1s \
NATLISTENER_TCP_PUNCH_DIAL_TIMEOUT=5s \
go run .
```

验证顺序：

1. 先测本机：`tcping 127.0.0.1 19880`。
2. 再测 LAN：`tcping 192.168.10.204 19880`。
3. 看程序打印的 external，例如 `223.73.225.207:59768`。
4. 最后测公网随机端口：`tcping -4 223.73.225.207 59768`。

保活和租约：

- `qq.com:80` 这条 TCP 连接是外层 CGNAT 保活。它会持续占住运营商 NAT 分配的公网随机端口。
- 路由器里显示的 `nattraversal` 剩余时间是 UPnP/NAT-PMP 内层映射租约，不是 qq.com 连接时长。
- 当前库默认申请 90 分钟内层映射，并每 45 分钟续租一次；程序正常 `Close()` 时会删除映射。
- `NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL=1s` 适合测试和激进网络。生产环境通常可以调到 `15s` 或 `30s`，但如果运营商 NAT 很快回收空闲 TCP，就继续用 `1s`。
- 只保持 qq.com TCP 连接还不够；如果内层 UPnP/NAT-PMP 映射过期，公网流量到了路由器 WAN 口也转不到本机。

Example:

```bash
NATLISTENER_TCP_PUNCH_ENABLE=true \
NATLISTENER_TCP_PUNCH_KEEPALIVE_ADDR=www.qq.com:80 \
NATLISTENER_TCP_PUNCH_STUN_ADDR=stun.nextcloud.com:3478 \
NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL=1s \
NATLISTENER_TCP_PUNCH_RETRY_INTERVAL=1s \
go run main.go
```

Optional variables:

| Variable | Description |
|----------|-------------|
| `NATLISTENER_TCP_PUNCH_LOCAL_IP` | Force the local IPv4 address used for the bound outbound socket |
| `NATLISTENER_TCP_PUNCH_DIAL_TIMEOUT` | Timeout for keepalive and STUN TCP dials (default: `5s`) |
| `NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL` | Interval between keepalive writes on the long-lived TCP connection (default: `30s`) |
| `NATLISTENER_TCP_PUNCH_RETRY_INTERVAL` | Delay before reconnecting the keepalive TCP connection after failure (default: `2s`) |
| `NATLISTENER_TCP_PUNCH_KEEPALIVE_PAYLOAD` | Payload written on each keepalive tick. `\r`, `\n`, and `\t` escapes are expanded |

When the keepalive server uses port 80 and no payload is configured, the mapper sends a small HTTP `HEAD` request by default. For other servers, configure an explicit payload if the server expects application data.

## Error Handling

The library provides descriptive error messages for common failure scenarios:
- No NAT traversal protocols available
- Port mapping failures
- External IP discovery issues
- Network connectivity problems

Example error handling:

The simplest approach is to use the built-in fallback functions:

```go
// ListenWithFallback automatically falls back to a standard listener
// if NAT traversal fails - no manual error handling needed!
listener, err := nattraversal.ListenWithFallback(8080)
if err != nil {
    log.Fatal("Failed to create listener:", err)
}
defer listener.Close()

if listener.IsFallback() {
    log.Println("Running in fallback mode (no NAT traversal)")
} else {
    log.Printf("Listening on external address: %s", listener.Addr())
}

for {
    conn, err := listener.Accept()
    if err != nil {
        log.Printf("Accept error: %v", err)
        continue
    }
    go handleConnection(conn)
}
```

For more control, you can handle errors manually:

```go
listener, err := nattraversal.Listen(8080)
if err != nil {
    log.Printf("NAT traversal failed: %v", err)
    // Fall back to local-only listener
    // Note: fallbackListener is net.Listener, not *NATListener
    fallbackListener, err := net.Listen("tcp", ":8080")
    if err != nil {
        log.Fatal("All listener creation failed:", err)
    }
    // Use the fallback listener directly (it implements net.Listener)
    defer fallbackListener.Close()
    for {
        conn, err := fallbackListener.Accept()
        if err != nil {
            log.Printf("Accept error: %v", err)
            continue
        }
        go handleConnection(conn)
    }
}
// Use NAT listener normally
defer listener.Close()
for {
    conn, err := listener.Accept()
    if err != nil {
        log.Printf("Accept error: %v", err)
        continue
    }
    go handleConnection(conn)
}
```

Alternatively, use the `net.Listener` interface for unified handling:

```go
var listener net.Listener
var err error

natListener, err := nattraversal.Listen(8080)
if err != nil {
    log.Printf("NAT traversal failed: %v, falling back to local listener", err)
    listener, err = net.Listen("tcp", ":8080")
    if err != nil {
        log.Fatal("All listener creation failed:", err)
    }
} else {
    listener = natListener
    log.Printf("Listening on external address: %s", natListener.Addr())
}
defer listener.Close()

for {
    conn, err := listener.Accept()
    if err != nil {
        log.Printf("Accept error: %v", err)
        continue
    }
    go handleConnection(conn)
}
```

## Limitations

- **IPv4 only**: This library only supports IPv4 networks. IPv6 environments are not supported due to NAT-PMP protocol limitations and gateway discovery mechanisms that rely on IPv4 addressing
- Requires router support for UPnP or NAT-PMP protocols
- TCP punching is TCP-only and requires both an inner router mapping and endpoint behavior that reuses the same upstream public port for connections from the same local port. Some symmetric or endpoint-dependent NATs will still fail.
- May not work with symmetric NAT configurations
- Firewall settings may block automatic port mapping
- Some corporate/restricted networks disable these protocols

## Dependencies

- `github.com/huin/goupnp` - UPnP protocol implementation
- `github.com/jackpal/go-nat-pmp` - NAT-PMP protocol implementation
- `github.com/go-i2p/logger` - Structured logging

## Logging

This library uses [`github.com/go-i2p/logger`](https://github.com/go-i2p/logger) for structured logging throughout. Logging is controlled entirely via environment variables — no code changes are needed.

### Environment Variables

| Variable | Values | Description |
|----------|--------|-------------|
| `DEBUG_I2P` | `debug`, `warn`, `error` | Sets log level (default: off) |
| `WARNFAIL_I2P` | any non-empty value | Causes all warnings and errors to call `log.Fatal` (developer fast-fail mode) |
| `NATLISTENER_TCP_PUNCH_ENABLE` | `true`, `1`, `yes`, `on` | Enables TCP punch mode for TCP listeners |
| `NATLISTENER_TCP_PUNCH_KEEPALIVE_ADDR` | `host:port` | TCP server used to keep the upstream NAT binding alive |
| `NATLISTENER_TCP_PUNCH_STUN_ADDR` | `host:port` | TCP STUN server used to discover the public endpoint |

### Usage Examples

```bash
# Run with no logs (default)
go run main.go

# Run with debug-level logs (shows all NAT discovery steps)
DEBUG_I2P=debug go run main.go

# Run with only warnings and errors
DEBUG_I2P=warn go run main.go

# Run tests with logging enabled
DEBUG_I2P=debug go test ./...

# Developer mode: fail immediately on any warning or error
WARNFAIL_I2P=true DEBUG_I2P=debug go test ./...
```

## License

See [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please ensure that:
- Code follows Go conventions and idioms
- Tests are included for new functionality
- Documentation is updated accordingly
- Changes maintain compatibility with existing APIs

