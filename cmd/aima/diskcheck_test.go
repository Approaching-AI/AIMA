package main

import "testing"

func TestEnoughDiskForDownload(t *testing.T) {
	tests := []struct {
		name             string
		requiredMiB      int64
		freeMiB          int64
		totalMiB         int64
		wantOK           bool
		wantShortfallMiB int64
	}{
		{
			name:        "plenty of space",
			requiredMiB: 5000, freeMiB: 200000, totalMiB: 500000,
			wantOK: true,
		},
		{
			// 5 GB download, only 6 GB free on a 100 GB disk: reserve is 10%
			// (10 GB), so it needs 15 GB but has 6 GB — would trip disk-pressure.
			name:        "download would trip disk-pressure",
			requiredMiB: 5000, freeMiB: 6000, totalMiB: 100000,
			wantOK: false, wantShortfallMiB: 9000,
		},
		{
			// Small disk: reserve floors at 2048 MiB rather than 10%.
			name:        "reserve floor on small disk",
			requiredMiB: 1000, freeMiB: 2500, totalMiB: 10000,
			wantOK: false, wantShortfallMiB: 548,
		},
		{
			// Unknown download size: cannot gate, must not false-block.
			name:        "unknown size does not gate",
			requiredMiB: 0, freeMiB: 100, totalMiB: 1000,
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, shortfall := enoughDiskForDownload(tt.requiredMiB, tt.freeMiB, tt.totalMiB)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok && shortfall != tt.wantShortfallMiB {
				t.Errorf("shortfall = %d, want %d", shortfall, tt.wantShortfallMiB)
			}
		})
	}
}

func TestDiskReserveMiB(t *testing.T) {
	if got := diskReserveMiB(500000); got != 50000 {
		t.Errorf("diskReserveMiB(500000) = %d, want 50000 (10%%)", got)
	}
	if got := diskReserveMiB(10000); got != 2048 {
		t.Errorf("diskReserveMiB(10000) = %d, want 2048 (floor)", got)
	}
}
