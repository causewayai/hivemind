package store

import "testing"

func TestVectorTable_InsertAndSearch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 4)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	near := make([]float32, 4)
	far := make([]float32, 4)
	for i := range near {
		near[i] = 0.1
		far[i] = 9.9
	}

	if err := s.insertVector(1, near); err != nil {
		t.Fatalf("insertVector(near) error = %v", err)
	}
	if err := s.insertVector(2, far); err != nil {
		t.Fatalf("insertVector(far) error = %v", err)
	}

	rowids, err := s.searchVectors(near, 1)
	if err != nil {
		t.Fatalf("searchVectors() error = %v", err)
	}
	if len(rowids) != 1 || rowids[0] != 1 {
		t.Errorf("searchVectors() = %v, want [1]", rowids)
	}
}
