package harness

import "github.com/dcalsky/best-harness-go/internal/protocol"

// ProtocolAdapter translates provider-neutral harness events into frames for
// an external wire protocol. Implementations are normally stateful and scoped
// to one logical Run; callers must not share an adapter between concurrent
// runs.
//
// Protocol adapters can implement this interface without coupling the core
// AgentEvent model to a frontend or agent-to-agent transport.
type ProtocolAdapter[Frame any] = protocol.Adapter[Frame]
