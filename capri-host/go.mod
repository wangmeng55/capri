module github.com/AgentsHarness/capri-host

go 1.26.2

require (
	github.com/coder/websocket v1.8.14
)

require github.com/AgentsHarness/capri-transport v0.0.0

replace github.com/AgentsHarness/capri-transport => ../capri-transport

