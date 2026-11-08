package main

import (
	"reflect"
	"testing"
)

func TestWatchedSetReplaces(t *testing.T) {
	var s watched
	s.Set([]uint32{7, 3})
	s.Set([]uint32{5})
	if got, want := s.IDs(), []uint32{5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func TestWatchedAttachedAdds(t *testing.T) {
	var s watched
	s.Set([]uint32{3})
	if !s.Attached(9) {
		t.Fatal("Attached(9) should report a change")
	}
	if s.Attached(9) {
		t.Fatal("Attached(9) again should report no change")
	}
	if got, want := s.IDs(), []uint32{3, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func TestWatchedDestroyedRemoves(t *testing.T) {
	var s watched
	s.Set([]uint32{3, 9})
	if !s.Destroyed(3) {
		t.Fatal("Destroyed(3) should report a change")
	}
	if got, want := s.IDs(), []uint32{9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func TestWatchedDestroyedUnknownIsNoop(t *testing.T) {
	var s watched
	s.Set([]uint32{3})
	if s.Destroyed(42) {
		t.Fatal("Destroyed of an unknown id should report no change")
	}
}

func TestWatchedEmpty(t *testing.T) {
	var s watched
	if got := s.IDs(); len(got) != 0 {
		t.Fatalf("IDs of an empty set = %v", got)
	}
	if got := s.Overflow(); got != 0 {
		t.Fatalf("Overflow of an empty set = %d", got)
	}
}
