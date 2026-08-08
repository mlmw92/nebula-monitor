package upgrade

import "testing"

func TestValidateVersionForUpload(t *testing.T) {
	tests := []struct {
		name, current, target, min string
		wantErr                    bool
	}{
		{"higher", "1.18.3", "1.19.0", "", false},
		{"same", "1.19.0", "1.19.0", "", true},
		{"lower", "1.19.0", "1.18.3", "", true},
		{"minimum incompatible", "1.18.0", "1.19.0", "1.18.1", true},
		{"minimum compatible", "1.18.3", "1.19.0", "1.18.1", false},
		{"dev", "dev", "1.19.0", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateVersionForUpload(tt.current, tt.target, tt.min); (err != nil) != tt.wantErr {
				t.Fatalf("validateVersionForUpload() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSafeComponentPath(t *testing.T) {
	if _, err := safeComponentPath("/tmp/unpacked", "../outside"); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	if _, err := safeComponentPath("/tmp/unpacked", "web/index.html"); err != nil {
		t.Fatalf("expected valid component path, got %v", err)
	}
}
