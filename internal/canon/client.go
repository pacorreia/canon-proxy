package canon

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Image struct {
	URL      string
	Filename string
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(host string, port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/ccapi/ver100/contents/sd/100CANON", nil)
	if err != nil {
		return nil, fmt.Errorf("build list request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list camera images: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list camera images returned status %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read image list response: %w", err)
	}

	urls, err := parseImageURLs(body)
	if err != nil {
		return nil, fmt.Errorf("parse image list response: %w", err)
	}

	images := make([]Image, 0, len(urls))
	for _, u := range urls {
		images = append(images, Image{URL: u, Filename: filenameFromURL(u)})
	}

	return images, nil
}

func (c *Client) DownloadImage(ctx context.Context, image Image) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, image.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request for %s: %w", image.URL, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image %s: %w", image.URL, err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("download image %s returned status %s", image.URL, resp.Status)
	}

	return resp.Body, nil
}

func parseImageURLs(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	inURL := false
	urls := []string{}

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode xml token: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "url" {
				inURL = true
			}
		case xml.EndElement:
			if t.Name.Local == "url" {
				inURL = false
			}
		case xml.CharData:
			if inURL {
				u := strings.TrimSpace(string(t))
				if u != "" {
					urls = append(urls, u)
				}
			}
		}
	}

	return urls, nil
}

func filenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return path.Base(raw)
	}
	return path.Base(u.Path)
}
