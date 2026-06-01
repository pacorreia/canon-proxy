package canon

import "testing"

func TestParseImageURLs(t *testing.T) {
	t.Parallel()

	xml := []byte(`<response><entry><url>http://camera/image1.jpg</url></entry><entry><url>http://camera/DCIM/100CANON/IMG_0002.JPG</url></entry></response>`)
	urls, err := parseImageURLs(xml)
	if err != nil {
		t.Fatalf("parse image urls: %v", err)
	}

	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if urls[0] != "http://camera/image1.jpg" {
		t.Fatalf("unexpected first url: %s", urls[0])
	}
	if got := filenameFromURL(urls[1]); got != "IMG_0002.JPG" {
		t.Fatalf("expected filename IMG_0002.JPG, got %s", got)
	}
}
