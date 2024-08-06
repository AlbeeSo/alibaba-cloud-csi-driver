package utils

import "testing"

func Test_SplitVolumeHandle(t *testing.T) {
	tests := []struct {
		volumeHandle       string
		expectedVolID      string
		expectedTargetType string
	}{
		{"pvname-snapshot", "pvname", "snapshot"},
		{"pv-name-overlaybd", "pv-name", "overlaybd"},
		{"fake", "", ""},
		{GetVolumeHandle("pvname", VolumeTypeFastImage), "pvname", VolumeTypeFastImage},
		{GetVolumeHandle("pv-name", VolumeTypeFastImage), "pv-name", VolumeTypeFastImage},
	}

	for _, test := range tests {
		volID, targetType := SplitVolumeHandle(test.volumeHandle)
		if volID != test.expectedVolID || targetType != test.expectedTargetType {
			t.Errorf("SplitVolumeHandle(%s) = %s, %s; expected %s, %s",
				test.volumeHandle, volID, targetType, test.expectedVolID, test.expectedTargetType)
		}
	}
}
