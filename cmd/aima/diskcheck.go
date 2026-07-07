package main

// diskReserveMiB is the free space kept beyond a model download so the node
// does not cross k3s's disk-pressure eviction threshold (kubelet defaults evict
// at nodefs<10% / imagefs<15%, which taints the node NoSchedule and leaves pods
// stuck Pending). Reserve 10% of the volume, with a 2 GiB floor for small disks.
func diskReserveMiB(totalMiB int64) int64 {
	reserve := totalMiB / 10
	if reserve < 2048 {
		reserve = 2048
	}
	return reserve
}

// enoughDiskForDownload reports whether a download of requiredMiB fits on a
// filesystem with freeMiB free / totalMiB total while preserving the reserve.
// requiredMiB<=0 means the size is unknown, in which case it cannot gate and
// returns true (best-effort). Returns the shortfall in MiB when it does not fit.
func enoughDiskForDownload(requiredMiB, freeMiB, totalMiB int64) (ok bool, shortfallMiB int64) {
	if requiredMiB <= 0 {
		return true, 0
	}
	need := requiredMiB + diskReserveMiB(totalMiB)
	if freeMiB >= need {
		return true, 0
	}
	return false, need - freeMiB
}
