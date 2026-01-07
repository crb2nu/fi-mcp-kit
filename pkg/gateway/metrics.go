package gateway

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HostsConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mcp_gateway_hosts_connected",
		Help: "The total number of currently connected MCP hosts",
	})

	ClientsConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mcp_gateway_clients_connected",
		Help: "The total number of currently connected MCP clients",
	})

	MessagesRelayed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_messages_relayed_total",
		Help: "The total number of MCP messages relayed through the gateway",
	}, []string{"direction", "host"})

	RelayErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_relay_errors_total",
		Help: "The total number of errors encountered while relaying messages",
	}, []string{"host"})
)
