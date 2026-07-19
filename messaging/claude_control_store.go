package messaging

// v4 removes the persisted local/remote owner map. Frontends only persist their
// own binding; exclusive write access is an in-process session lease held for a
// single prompt lifecycle.
const claudeSessionStateVersion = 4
