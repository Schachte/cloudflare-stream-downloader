package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/grafov/m3u8"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Please provide an HLS manifest URL as the first argument.")
		return
	}

	manifestURL := os.Args[1]
	chosenManifestURL, resolution, err := downloadAndParse(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem downloading the manifest: %v", err)
	}

	prefixURL, UID, err := extractUIDAndPrefixURL(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem parsing the base url: %v", err)
	}

	fmt.Println("Starting download...")

	segmentPaths, err := downloadSegmentsFromManifest(chosenManifestURL, prefixURL, UID, resolution)
	if err != nil {
		log.Fatalf("there was a problem downloading the segments: %v", err)
	}

	err = concatenateTSFiles(
		segmentPaths,
		fmt.Sprintf("%s/%s", UID, resolution),
		fmt.Sprintf("%s.mp4", UID),
	)
	if err != nil {
		log.Fatalf("there was a problem concatenating the segments: %v", err)
	}

	fmt.Println("Complete!")
	fmt.Println("---------------------------------------------")
	fmt.Printf("Output [video] will be in the directory\n./%s/%s/%s.mp4\n\n", UID, resolution, UID)
	fmt.Printf("Output [segments] will be in the directory\n./%s/%s/segments/\n", UID, resolution)
	fmt.Println("---------------------------------------------")
}

// downloadSegmentsFromManifest will download a complete video and individual segments
// from a particular manifest and returns the list of relative segment paths
func downloadSegmentsFromManifest(manifestURL, baseURL, UID, resolution string) ([]string, error) {
	resp, err := http.Get(manifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	dataBuf := bytes.NewBuffer(body)
	playlist, listType, err := m3u8.Decode(*dataBuf, false)
	if err != nil {
		return nil, err
	}

	writer := bufio.NewWriter(os.Stdout)
	localSegmentPaths := []string{}
	if listType == m3u8.MEDIA {
		lastReportedProgress := 0
		mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
		totalSegments := len(mediaPlaylist.Segments)
		for idx, segment := range mediaPlaylist.Segments {
			if segment != nil {
				segmentURL := segment.URI
				for strings.HasPrefix(segmentURL, "../") {
					segmentURL = strings.TrimPrefix(segmentURL, "../")
				}
				completeSegmentURL := fmt.Sprintf("%s/%s", baseURL, segmentURL)
				segmentName, err := getSegmentName(completeSegmentURL)
				if err != nil {
					return nil, err
				}
				localSegmentPath := fmt.Sprintf("%s/%s/segments/%s", UID, resolution, segmentName)
				localSegmentPaths = append(localSegmentPaths, localSegmentPath)
				err = downloadFile(completeSegmentURL, localSegmentPath)
				if err != nil {
					return nil, err
				}
			}

			// user-friendly progress output
			prog := int((float32(idx) / float32(totalSegments)) * 100)
			if prog%5 == 0 && prog != lastReportedProgress {
				msg := fmt.Sprintf("%d%% complete\n", prog)
				fmt.Fprint(writer, msg)
				writer.Flush()
				lastReportedProgress = prog
			}
		}
	}
	return localSegmentPaths, nil
}

// extractUIDAndPrefixURL will parse out the base URI for the customer as well
// as the UID for the video
func extractUIDAndPrefixURL(url string) (baseURL, uid string, err error) {
	regex := regexp.MustCompile(`^(.+)/([a-f0-9]+)/manifest/video.m3u8$`)
	matches := regex.FindStringSubmatch(url)

	if len(matches) == 3 {
		baseURI := matches[1]
		uid := matches[2]
		return baseURI, uid, nil
	}
	return "", "", errors.New("invalid input")
}

// downloadAndParse will allow the user to select a resolution to download and return
// the resultant manifest URL for that resolution
func downloadAndParse(url string) (resManifest, resolution string, err error) {
	baseURL, UID, err := extractUIDAndPrefixURL(url)
	if err != nil {
		return "", "", err
	}
	resp, err := http.Get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	dataBuf := bytes.NewBuffer(body)
	playlist, _, err := m3u8.Decode(*dataBuf, false)
	if err != nil {
		return "", "", err
	}

	masterPlaylist := playlist.(*m3u8.MasterPlaylist)
	resolutionURLs := make(map[string]string)

	fmt.Printf("Listing all available resolutions for video UID: %s\n\n", UID)
	var userOption int
	manifestURLIdx := []string{}
	resolutionIdx := []string{}

	for idx, variant := range masterPlaylist.Variants {
		manifestForResolution := fmt.Sprintf("%s/%s/manifest/%s", baseURL, UID, variant.URI)
		resolutionURLs[variant.Resolution] = manifestForResolution
		manifestURLIdx = append(manifestURLIdx, manifestForResolution)
		resolutionIdx = append(resolutionIdx, variant.Resolution)
		fmt.Printf("%d) %s\n", idx, variant.Resolution)
	}

	fmt.Printf("%d) Exit\n", len(manifestURLIdx))
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nSelect which resolution you'd like to download: ")
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading input:", err)
		return "", "", err
	}

	userOption, err = strconv.Atoi(input[:len(input)-1])
	if err != nil {
		fmt.Println("Error converting input to integer:", err)
		return "", "", err
	}

	if userOption == len(manifestURLIdx) {
		fmt.Println("Exiting Stream downloader")
		os.Exit(1)
	}

	chosenResolution := resolutionIdx[userOption]
	fmt.Printf("Beginning download for: %s\n", chosenResolution)

	return resolutionURLs[chosenResolution], chosenResolution, nil
}

// downloadFile will take a URL and download it to a predfined location
func downloadFile(url, relativePath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	targetDir := filepath.Dir(relativePath)
	err = os.MkdirAll(targetDir, 0755)
	if err != nil {
		return err
	}

	out, err := os.Create(relativePath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// getSegmentName will strip out the unique segment name from a segment
// request URL
func getSegmentName(urlStr string) (string, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	base := path.Base(parsedURL.Path)
	segmentPattern := regexp.MustCompile(`^seg_\d+\.ts$`)
	if segmentPattern.MatchString(base) {
		return base, nil
	}
	return "", fmt.Errorf("segment name not found")
}

// concatenateTSFiles take all downloaded segments and concat into single, playable
// mp4 using ffmpeg
func concatenateTSFiles(tsFiles []string, outputDir, outputFilename string) error {
	tempFile, err := os.CreateTemp("", "tslist-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())

	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, tsFile := range tsFiles {
		segmentPath := fmt.Sprintf("%s/%s", currentDir, tsFile)
		_, err := tempFile.WriteString(fmt.Sprintf("file '%s'\n", segmentPath))
		if err != nil {
			return err
		}
	}
	tempFile.Close()

	err = os.MkdirAll(outputDir, 0755)
	if err != nil {
		return err
	}

	outputPath := path.Join(outputDir, outputFilename)
	cmd := exec.Command("ffmpeg", "-f", "concat", "-safe", "0", "-i", tempFile.Name(), "-c", "copy", outputPath)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("ffmpeg error: %w", err)
	}

	return nil
}
