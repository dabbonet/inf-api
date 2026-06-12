package handler

import "testing"

func TestShouldSkipIntroDelta_FirstPass(t *testing.T) {
	h := &streamHandler{introDedup: make(map[string]struct{})}

	if h.shouldSkipIntroDelta("I am Claude") {
		t.Fatalf("first intro delta should not be skipped")
	}
}

func TestShouldSkipIntroDelta_Duplicate(t *testing.T) {
	h := &streamHandler{introDedup: make(map[string]struct{})}

	if h.shouldSkipIntroDelta("hello") {
		t.Fatalf("first intro delta should not be skipped")
	}
	if !h.shouldSkipIntroDelta("hello") {
		t.Fatalf("duplicate intro delta should be skipped")
	}
}
