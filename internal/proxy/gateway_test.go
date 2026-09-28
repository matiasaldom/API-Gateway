package proxy

import "testing"

func TestRefuseUnsafeDial(t *testing.T) {
	for addr, wantErr := range map[string]bool{
		"169.254.169.254:80":      true, // cloud metadata
		"[fe80::1]:8080":          true,
		"[::ffff:169.254.1.1]:80": true,
		"0.0.0.0:8081":            true,
		"127.0.0.1:8081":          false, // local upstreams are allowed
		"10.0.0.5:80":             false, // so are private-network services
		"[2001:db8::1]:443":       false,
	} {
		if err := refuseUnsafeDial("tcp", addr, nil); (err != nil) != wantErr {
			t.Errorf("%s: err = %v, want error %v", addr, err, wantErr)
		}
	}
}
