package utils

import (
	"encoding/base64"
	"reflect"
	"testing"
)

func Test_setDefault(t *testing.T) {
	tests := []struct {
		name     string
		vc       map[string]string
		expected bool
		wantErr  bool
	}{
		{
			name:     "VALUEMUSTHAVE already exists",
			vc:       map[string]string{KeyTargetRef: "existingValue"},
			expected: true,
			wantErr:  false,
		},
		{
			name:     "VALUEMUSTHAVE not exists",
			vc:       map[string]string{},
			expected: false,
			wantErr:  true,
		},
		{
			name: "all required fields set",
			vc: map[string]string{
				KeyVolumeType: VolumeTypeFastImage,
				KeyFsType:     FsTypeEXT4,
				KeyTargetRef:  "existingValue",
				KeyReadOnly:   "true",
			},
			expected: false,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setDefault(tt.vc)
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

func Test_ValidateNodePulishVolumeSecrets(t *testing.T) {
	tests := []struct {
		secret   map[string]string
		wantType string
		wantAuth string
	}{
		{
			secret: map[string]string{
				SecretAccessKeyId:     "abc123",
				SecretAccessKeySecret: "def456",
			},
			wantType: AliyunAK,
			wantAuth: base64.StdEncoding.EncodeToString([]byte("abc123:def456")),
		},
		{
			secret: map[string]string{
				SecretUsername: "user",
				SecretPassword: "pass",
			},
			wantType: DockerAuth,
			wantAuth: base64.StdEncoding.EncodeToString([]byte("abc123:def456")),
		},
		{
			secret: map[string]string{
				SecretAccessKeyId: "abc123",
			},
			wantType: "",
			wantAuth: "",
		},
		{
			secret:   map[string]string{},
			wantType: "",
			wantAuth: "",
		},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if gotType, gotAuth := ValidateNodeStageVolumeSecrets(tt.secret); gotType != tt.wantType || gotAuth != tt.wantAuth {
				t.Errorf("ValidateNodeStageVolumeSecrets() = %v, %v, want %v, %v", gotType, gotAuth, tt.wantType, tt.wantAuth)
			}
		})
	}
}
