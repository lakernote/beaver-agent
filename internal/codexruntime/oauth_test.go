package codexruntime

import "testing"

func TestParseMCPOAuthStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		payload    string
		authorized bool
		wantErr    bool
	}{
		{name: "authorized", payload: `{"auth_status":"o_auth"}`, authorized: true},
		{name: "not logged in", payload: `{"auth_status":"not_logged_in"}`},
		{name: "unsupported", payload: `{"auth_status":"unsupported"}`},
		{name: "malformed", payload: `{`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorized, _, err := parseMCPOAuthStatus([]byte(test.payload))
			if (err != nil) != test.wantErr || authorized != test.authorized {
				t.Fatalf("parseMCPOAuthStatus() = authorized %v, err %v", authorized, err)
			}
		})
	}
}

func TestParseMCPOAuthStatusForName(t *testing.T) {
	authorized, detail, err := parseMCPOAuthStatusForName([]byte(`[{"name":"other","auth_status":"not_logged_in"},{"name":"gitlab","auth_status":"o_auth"}]`), "gitlab")
	if err != nil || !authorized || detail == "" {
		t.Fatalf("parseMCPOAuthStatusForName() = authorized %v detail %q err %v", authorized, detail, err)
	}
	unauthorized, _, err := parseMCPOAuthStatusForName([]byte(`[{"name":"other","auth_status":"o_auth"}]`), "gitlab")
	if err != nil || unauthorized {
		t.Fatalf("missing server must not be reported authorized: authorized %v err %v", unauthorized, err)
	}
}
