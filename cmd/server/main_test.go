package main

import (
	"net/http"
	"testing"
)

func TestTunnelNameValidation(t *testing.T) {
	valid := []string{"demo", "my-tunnel", "A1", "abc-123"}
	for _, name := range valid {
		if !nameRE.MatchString(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	invalid := []string{"", "-bad", "bad/name", "with space", "a?b", "this-name-is-way-too-long-for-the-limit-123"}
	for _, name := range invalid {
		if nameRE.MatchString(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestCleanHeadersDropsHopByHopAndHost(t *testing.T) {
	in := http.Header{}
	in.Set("Connection", "keep-alive")
	in.Set("Host", "example.test")
	in.Set("Content-Length", "4")
	in.Set("X-Request-ID", "abc")
	got := cleanHeaders(in)
	if _, ok := got["Connection"]; ok {
		t.Fatal("Connection must be removed")
	}
	if _, ok := got["Host"]; ok {
		t.Fatal("Host must be removed")
	}
	if _, ok := got["Content-Length"]; ok {
		t.Fatal("Content-Length must be removed")
	}
	if got["X-Request-Id"][0] != "abc" {
		t.Fatal("expected safe header to remain")
	}
}
