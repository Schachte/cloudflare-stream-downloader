package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/schollz/progressbar/v3"
)

var (
	AccountID         = os.Getenv("STREAM_ACCOUNT") // replace with your Cloudflare account ID
	API_KEY           = os.Getenv("STREAM_API_KEY") // replace with your Cloudflare API key
	ENDPOINT_OVERRIDE = os.Getenv("CLOUDFLARE_URL") // optional endpoint override for upload destination
)

var (
	ChunkSize      = int64(5) * 1024 * 1024 // 5MB
	CloudflareURL  = fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/stream", AccountID)
	CloudflareAuth = fmt.Sprintf("Bearer %s", API_KEY)
)

// initUpload invokes a TUS upload against Cloudflare Stream with a given local
// file path
func initUpload(filePath string) {
	if AccountID == "" {
		log.Fatal("Set you cloudflare account ID as env var STREAM_ACCOUNT")
	}
	if API_KEY == "" {
		log.Fatal("Set you cloudflare API key as env var STREAM_API_KEY")
	}

	file, err := os.Open(filePath)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		panic(err)
	}

	filename := filepath.Base(file.Name())
	inputBytes := []byte(filename)
	encodedFileName := base64.StdEncoding.EncodeToString(inputBytes)

	uploadURL, err := createUpload(fileInfo.Size(), encodedFileName)
	if err != nil {
		panic(err)
	}

	chunkCount := (fileInfo.Size() + ChunkSize - 1) / ChunkSize
	bar := progressbar.Default(chunkCount)
	for i := int64(0); i < chunkCount; i++ {
		uploadOffset, err := getUploadOffset(uploadURL)
		if err != nil {
			panic(err)
		}
		uploadChunk(file, uploadURL, uploadOffset)
		bar.Add(1)
	}
}

func createUpload(fileSize int64, encodedFilename string) (string, error) {

	var req *http.Request
	var err error
	if ENDPOINT_OVERRIDE != "" {
		req, err = http.NewRequest("POST", ENDPOINT_OVERRIDE, nil)
		if err != nil {
			return "", err
		}
	} else {
		req, err = http.NewRequest("POST", CloudflareURL, nil)
		if err != nil {
			return "", err
		}
	}

	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Authorization", CloudflareAuth)
	req.Header.Set("Upload-Length", fmt.Sprintf("%d", fileSize))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Metadata", fmt.Sprintf("name %s", encodedFilename))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), nil
}

func getUploadOffset(uploadURL string) (int64, error) {
	req, err := http.NewRequest("HEAD", uploadURL, nil)
	if err != nil {
		return -1, err
	}

	req.Header.Set("Authorization", CloudflareAuth)
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	uploadOffset, err := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return -1, err
	}
	return uploadOffset, nil
}

func uploadChunk(file *os.File, uploadURL string, uploadOffset int64) (int, error) {
	buf := make([]byte, ChunkSize)
	n, err := file.ReadAt(buf, uploadOffset)
	if err != nil && err != io.EOF {
		return -1, err
	}

	req, err := http.NewRequest("PATCH", uploadURL, bytes.NewReader(buf[:n]))
	if err != nil {
		return -1, err
	}

	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Authorization", CloudflareAuth)
	req.Header.Set("Upload-Offset", fmt.Sprintf("%d", uploadOffset))
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		log.Println(fmt.Errorf("unexpected status code: %d", resp.StatusCode))
		return -1, fmt.Errorf("invalid response code: %d", resp.StatusCode)
	}

	return n, nil
}
