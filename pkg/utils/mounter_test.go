/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormat(t *testing.T) {

	testSource := ".tmp/"
	testFsType := "ext4test"
	testMounter := NewMounter()
	err := testMounter.Format(testSource, testFsType)
	assert.NotNil(t, err)
	testFsType = "ext4"
	err = testMounter.Format(testSource, testFsType)
	assert.NotNil(t, err)
}

func TestMount(t *testing.T) {
	mountErrDir := ".tmp/"
	mountedDevice := ".mounted/block"
	testMounter := NewMounter()
	err := testMounter.EnsureFolder(mountErrDir)
	assert.Nil(t, err)
	err = testMounter.MountBlock(mountedDevice, mountErrDir)
	assert.NotNil(t, err)

}

// Test_hasMountOption tests the hasMountOption function
func Test_hasMountOption(t *testing.T) {
	tests := []struct {
		options []string
		opt     string
		want    bool
	}{
		{[]string{}, "option", false},
		{[]string{"option1", "option2", "option3"}, "option2", true},
		{[]string{"option1", "option2", "option3"}, "option4", false},
	}

	for _, tt := range tests {
		t.Run(tt.opt, func(t *testing.T) {
			got := hasMountOption(tt.options, tt.opt)
			if got != tt.want {
				t.Errorf("hasMountOption(%v, %v) = %v, want %v", tt.options, tt.opt, got, tt.want)
			}
		})
	}
}

// Test_CollectMountOptions tests the CollectMountOptions function
func Test_CollectMountOptions(t *testing.T) {

	tests := []struct {
		fsType   string
		mntFlags []string
		want     []string
	}{
		{FsTypeXFS, []string{}, []string{NOUUID}},
		{FsTypeXFS, []string{NOUUID}, []string{NOUUID}},
		{FsTypeXFS, []string{"ro", "rw"}, []string{"ro", "rw", NOUUID}},
		{"ext4", []string{NOUUID}, []string{NOUUID}},
		{"ext4", []string{"ro", "rw"}, []string{"ro", "rw"}},
	}

	for _, tt := range tests {
		t.Run(tt.fsType, func(t *testing.T) {
			got := CollectMountOptions(tt.fsType, tt.mntFlags)
			if len(got) != len(tt.want) {
				t.Errorf("CollectMountOptions(%v, %v) = %v, want %v", tt.fsType, tt.mntFlags, got, tt.want)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("CollectMountOptions(%v, %v)[%d] = %v, want %v", tt.fsType, tt.mntFlags, i, v, tt.want[i])
				}
			}
		})
	}
}
