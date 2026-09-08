package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrames(t *testing.T) {
	var b bytes.Buffer
	payload := bytes.Repeat([]byte("x"), ChunkSize)
	if err := Write(&b, DataFrame, payload); err != nil {
		t.Fatal(err)
	}
	f, err := Read(&b)
	if err != nil || f.Type != DataFrame || !bytes.Equal(f.Data, payload) {
		t.Fatalf("roundtrip: %v", err)
	}
}
func TestRejectMalformed(t *testing.T) {
	cases := [][]byte{{'R', 'L', 2, DataFrame, 0, 0, 0, 0}, {'R', 'L', 1, 99, 0, 0, 0, 0}, {'R', 'L', 1, DataFrame, 255, 255, 255, 255}, {'R', 'L', 1, DataFrame, 0, 0, 0, 2, 1}}
	for _, b := range cases {
		if _, err := Read(bytes.NewReader(b)); err == nil {
			t.Fatalf("accepted %v", b)
		}
	}
	var b bytes.Buffer
	if err := Write(&b, DataFrame, make([]byte, ChunkSize+1)); err == nil {
		t.Fatal("oversized write accepted")
	}
}
func FuzzRead(f *testing.F) {
	f.Add([]byte{'R', 'L', 1, DataFrame, 0, 0, 0, 1, 42})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Read(bytes.NewReader(b)) })
}
func BenchmarkDataFrame(b *testing.B) {
	payload := make([]byte, ChunkSize)
	encoded := make([]byte, 8+len(payload))
	copy(encoded, []byte{'R', 'L', 1, DataFrame})
	binary.BigEndian.PutUint32(encoded[4:], uint32(len(payload)))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Read(bytes.NewReader(encoded)); err != nil {
			b.Fatal(err)
		}
	}
}
