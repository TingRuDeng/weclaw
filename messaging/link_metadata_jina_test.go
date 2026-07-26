package messaging

import "testing"

func TestJinaReaderURLEscapesEntireTargetURLAsOnePathSegment(t *testing.T) {
	rawURL := "https://example.com/private path?q=secret&lang=中文#section"
	want := "https://r.jina.ai/https:%2F%2Fexample.com%2Fprivate%20path%3Fq=secret&lang=%E4%B8%AD%E6%96%87%23section"
	if got := jinaReaderURL(rawURL); got != want {
		t.Fatalf("jinaReaderURL()=%q, want %q", got, want)
	}
}
