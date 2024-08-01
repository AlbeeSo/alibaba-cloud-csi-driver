package utils

import (
	"reflect"
	"testing"
)

func Test_setDefault(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		vc       map[string]string
		expected bool
		wantErr  bool
	}{
		{
			name:     "Key already exists",
			key:      "existingKey",
			value:    "existingValue",
			vc:       map[string]string{"existingKey": "existingValue"},
			expected: false,
			wantErr:  false,
		},
		{
			name:     "Value is VALUEMUSTHAVE",
			key:      "mustHaveKey",
			value:    VALUEMUSTHAVE,
			vc:       map[string]string{},
			expected: false,
			wantErr:  true,
		},
		{
			name:     "Value is VALUEOPTIONAL",
			key:      "optionalKey",
			value:    VALUEOPTIONAL,
			vc:       map[string]string{},
			expected: false,
			wantErr:  false,
		},
		{
			name:     "Value is not VALUEMUSTHAVE or VALUEOPTIONAL",
			key:      "customKey",
			value:    "customValue",
			vc:       map[string]string{},
			expected: true,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setDefault(tt.key, tt.value, tt.vc)
			if (err != nil) != tt.wantErr {
				t.Errorf("setDefault() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("setDefault() got = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func Test_ValidateCreateVolumeParams(t *testing.T) {
	tests := []struct {
		name     string
		vc       map[string]string
		expected bool
		wantErr  bool
		wantVc   map[string]string
	}{
		{
			name: "All required fields set",
			vc: map[string]string{
				KeyVolumeType:      VolumeTypeFastImage,
				KeySecretName:      "name",
				KeySecretNamespace: "ns",
				KeyFsType:          FsTypeEXT4,
				KeyTargetType:      TargetTypeOSS,
				KeyTargetRef:       "targetRef",
				KeyReadOnly:        "true",
			},
			expected: false,
			wantErr:  false,
			wantVc: map[string]string{
				KeyVolumeType:          VolumeTypeFastImage,
				ProvSecretNameKey:      "name",
				ProvSecretNamespaceKey: "ns",
				KeyFsType:              FsTypeEXT4,
				KeyTargetType:          TargetTypeOSS,
				KeyTargetRef:           "targetRef",
				KeyReadOnly:            "true",
			},
		},
		{
			name: "Missing required field",
			vc: map[string]string{
				KeyVolumeType:      VolumeTypeFastImage,
				KeySecretName:      "akID",
				KeySecretNamespace: "akSecret",
				KeyFsType:          FsTypeEXT4,
				KeyTargetType:      TargetTypeOSS,
				KeyReadOnly:        "true",
			},
			expected: false,
			wantErr:  true,
		},
		{
			name: "Missing optional field",
			vc: map[string]string{
				KeyTargetRef: "targetRef",
			},
			expected: true,
			wantErr:  false,
			wantVc: map[string]string{
				KeyVolumeType: VolumeTypeFastImage,
				KeyFsType:     FsTypeEXT4,
				KeyTargetType: TargetTypeOSS,
				KeyTargetRef:  "targetRef",
				KeyReadOnly:   "true",
			},
		},
		{
			name: "Missing one of secret reference",
			vc: map[string]string{
				KeyVolumeType:      VolumeTypeFastImage,
				KeySecretNamespace: "ns",
				KeyFsType:          FsTypeEXT4,
				KeyTargetType:      TargetTypeOSS,
				KeyTargetRef:       "targetRef",
				KeyReadOnly:        "true",
			},
			expected: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified, err := ValidateCreateVolumeParams(tt.vc)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreateVolumeParams() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if modified != tt.expected {
				t.Errorf("ValidateCreateVolumeParams() modified = %v, expected %v", modified, tt.expected)
			}
			if tt.wantVc != nil && reflect.DeepEqual(tt.vc, tt.wantVc) {
				t.Errorf("ValidateCreateVolumeParams() vc = %v, expected %v", tt.vc, tt.wantVc)
			}
		})
	}
}
