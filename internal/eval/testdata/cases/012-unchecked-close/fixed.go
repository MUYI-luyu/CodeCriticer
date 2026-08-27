package main

import (
	"fmt"
	"io"
	"net/http"
)

func FetchURL(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			err = cerr
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func main() {
	data, err := FetchURL("https://example.com")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Fetched %d bytes\n", len(data))
}
