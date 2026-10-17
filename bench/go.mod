module github.com/AgentsHarness/capri-bench

go 1.26.2

require (
	github.com/AgentsHarness/capri-host v0.0.0
	github.com/AgentsHarness/capri-hub v0.0.0
	github.com/AgentsHarness/capri-transport v0.0.0
)

replace (
	github.com/AgentsHarness/capri-host => ../capri-host
	github.com/AgentsHarness/capri-hub => ../capri-hub
	github.com/AgentsHarness/capri-transport => ../capri-transport
)
