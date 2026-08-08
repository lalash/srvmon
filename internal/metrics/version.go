package metrics

// AgentVersion is the version of the agent binary, and it is deliberately not
// the hub's version. Bump it only when something under cmd/agent or
// internal/metrics actually changes.
//
// Tying it to the release version would mark every agent out of date after a
// dashboard-only release, so the panel would offer to replace a binary on every
// monitored host for a change that never touched it — the riskiest operation in
// the system, for nothing.
const AgentVersion = "1.1.0"
