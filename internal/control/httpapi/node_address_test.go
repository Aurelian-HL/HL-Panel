package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestObservedNodeIPTrustsOnlyLoopbackProxy(t *testing.T) {
	for _, test := range []struct{ peer, real, want string }{
		{"8.8.8.8:1234", "1.1.1.1", "8.8.8.8"},
		{"127.0.0.1:1234", "1.1.1.1", "1.1.1.1"},
		{"[::1]:1234", "2606:4700:4700::1111", "2606:4700:4700::1111"},
		{"127.0.0.1:1234", "1.1.1.1,8.8.8.8", ""},
		{"127.0.0.1:1234", "10.1.2.3", ""},
		{"127.0.0.1:1234", "", ""},
		{"10.1.2.3:1234", "8.8.8.8", ""},
		{"[2606:4700:4700::1111]:1234", "8.8.8.8", "2606:4700:4700::1111"},
		{"invalid", "8.8.8.8", ""},
	} {
		request := httptest.NewRequest("POST", "/api/v1/agent/heartbeat", nil)
		request.RemoteAddr = test.peer
		request.Header.Set("X-Real-IP", test.real)
		request.Header.Set("X-Forwarded-For", "8.8.8.8")
		if got := observedNodeIP(request); got != test.want {
			t.Errorf("peer=%s real=%s: got %s, want %s", test.peer, test.real, got, test.want)
		}
	}
}
