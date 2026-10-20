package snapshotowner

// InstallSyntheticOwnerEvidence is available only in this package's test variant.
// It leaves filesystem protection real but replaces every process observation.
func InstallSyntheticOwnerEvidence(pid int, dead func(int) (bool, error)) func() {
	oldPID, oldDead := ownerPID, ownerProcessDead
	ownerPID, ownerProcessDead = func() int { return pid }, dead
	return func() { ownerPID, ownerProcessDead = oldPID, oldDead }
}
