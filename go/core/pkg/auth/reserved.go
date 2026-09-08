package auth

// ScheduledRunUserID authenticates scheduler work and owns unbound conversations. It is
// accepted for control-plane ownership only on internal calls. Runtime memory
// callbacks may carry it without acquiring conversation access.
const ScheduledRunUserID = "scheduled-run"
