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
	"github.com/manifoldco/promptui"
)

const (
	OPTION_DOWNLOAD            = "Download video and segments"
	OPTION_LIST_RESOLUTIONS    = "List available resolutions"
	OPTION_COUNT_SEGMENTS      = "Count number of segments"
	OPTION_OUTPUT_MANIFEST_URL = "Output m3u8 manifest URL for a specific resolution"
	OPTION_CHANGE_MANIFEST_URL = "Update manifest URL"
	OPTION_EXIT                = "🚫 Exit"
)

type Video struct {
	BaseURL            string
	MasterManifestURL  string
	VideoUID           string
	RenditionManifests map[string]string

	MasterPlaylist m3u8.MasterPlaylist
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Please provide an HLS manifest URL as the first argument.")
		return
	}
	manifestURL := os.Args[1]
	for {
		prompt := promptui.Select{
			Label: "Cloudflare Stream Downloader",
			Items: []string{
				OPTION_DOWNLOAD,
				OPTION_OUTPUT_MANIFEST_URL,
				OPTION_CHANGE_MANIFEST_URL,
				OPTION_LIST_RESOLUTIONS,
				OPTION_COUNT_SEGMENTS,
				OPTION_EXIT,
			},
		}

		_, result, err := prompt.Run()
		if err != nil {
			log.Fatal("Unable to process selection")
		}

		switch result {
		case OPTION_DOWNLOAD:
			initializeVideoDownloadProcess(manifestURL)
		case OPTION_OUTPUT_MANIFEST_URL:
			outputManifestURL(manifestURL)
		case OPTION_LIST_RESOLUTIONS:
			listAvailableResolutions(manifestURL)
		case OPTION_COUNT_SEGMENTS:
			countTotalSegments(manifestURL)
		case OPTION_CHANGE_MANIFEST_URL:
			fmt.Print("Enter new m3u8 manifest URL: ")
			var userInput string
			fmt.Scanln(&userInput)
			manifestURL = userInput
		case OPTION_EXIT:
			fmt.Println("👋 Exiting Stream downloader")
			os.Exit(1)
		default:
			fmt.Println("Option not available")
		}
	}
}

// outputManifestURL will output the m3u8 manifest URL for a specific video resolution
func outputManifestURL(manifestURL string) {
	baseURL, UID, err := extractUIDAndPrefixURL(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem parsing the base url: %v", err)
	}

	video := Video{
		MasterManifestURL: manifestURL,
		BaseURL:           baseURL,
		VideoUID:          UID,
	}

	masterPlaylist, err := video.retrieveMasterPlaylist(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem retrieving master playlist: %v", err)
	}
	video.MasterPlaylist = *masterPlaylist

	chosenManifest, _, err := video.printResolutionDownloadMenu()
	if err != nil {
		log.Fatalf("there was a problem selecting a download option: %v", err)
	}

	fmt.Println(chosenManifest)

	// segmentPaths, err := video.downloadSegmentsFromManifest(chosenManifest, chosenResolution, true)
	// if err != nil {
	// 	log.Fatalf("there was a problem downloading the segments: %v", err)
	// }

	// fmt.Printf("There are a total of %d segments on the %s manifest\n",
	// 	len(segmentPaths),
	// 	chosenResolution,
	// )
}

// countTotalSegments will output the number of segments on a particular manifest
func countTotalSegments(manifestURL string) {
	baseURL, UID, err := extractUIDAndPrefixURL(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem parsing the base url: %v", err)
	}

	video := Video{
		MasterManifestURL: manifestURL,
		BaseURL:           baseURL,
		VideoUID:          UID,
	}

	masterPlaylist, err := video.retrieveMasterPlaylist(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem retrieving master playlist: %v", err)
	}
	video.MasterPlaylist = *masterPlaylist

	chosenManifest, chosenResolution, err := video.printResolutionDownloadMenu()
	if err != nil {
		log.Fatalf("there was a problem selecting a download option: %v", err)
	}

	segmentPaths, err := video.downloadSegmentsFromManifest(chosenManifest, chosenResolution, true)
	if err != nil {
		log.Fatalf("there was a problem downloading the segments: %v", err)
	}

	fmt.Printf("There are a total of %d segments on the %s manifest\n",
		len(segmentPaths),
		chosenResolution,
	)
}

// listAvailableResolutions outputs all available resolutions from a manifest
func listAvailableResolutions(manifestURL string) {
	baseURL, UID, err := extractUIDAndPrefixURL(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem parsing the base url: %v", err)
	}

	video := Video{
		MasterManifestURL: manifestURL,
		BaseURL:           baseURL,
		VideoUID:          UID,
	}

	masterPlaylist, err := video.retrieveMasterPlaylist(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem retrieving master playlist: %v", err)
	}

	video.MasterPlaylist = *masterPlaylist
	_, _, err = video.printResolutionDownloadMenu()
	if err != nil {
		log.Fatalf("there was a problem selecting a download option: %v", err)
	}
}

// initializeVideoDownloadProcess will invoke the download job to pull
// all segments and final mp4 video onto disk
func initializeVideoDownloadProcess(manifestURL string) {
	baseURL, UID, err := extractUIDAndPrefixURL(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem parsing the base url: %v", err)
	}

	video := Video{
		MasterManifestURL: manifestURL,
		BaseURL:           baseURL,
		VideoUID:          UID,
	}

	masterPlaylist, err := video.retrieveMasterPlaylist(manifestURL)
	if err != nil {
		log.Fatalf("there was a problem retrieving master playlist: %v", err)
	}
	video.MasterPlaylist = *masterPlaylist

	chosenManifest, chosenResolution, err := video.printResolutionDownloadMenu()
	if err != nil {
		log.Fatalf("there was a problem selecting a download option: %v", err)
	}

	segmentPaths, err := video.downloadSegmentsFromManifest(chosenManifest, chosenResolution, false)
	if err != nil {
		log.Fatalf("there was a problem downloading the segments: %v", err)
	}

	err = video.concatenateTSFiles(segmentPaths, chosenResolution)
	if err != nil {
		log.Fatalf("there was a problem concatenating the segments: %v", err)
	}

	video.renderOutputPaths(chosenResolution)
}

// downloadSegmentsFromManifest will download a complete video and individual segments
// from a particular manifest and returns the list of relative segment paths
func (v *Video) downloadSegmentsFromManifest(manifestURL, resolution string, skipDownload bool) ([]string, error) {
	fmt.Printf("🌱 Beginning download for [%s]\n", resolution)
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
				completeSegmentURL := fmt.Sprintf("%s/%s", v.BaseURL, segmentURL)
				segmentName, err := getSegmentName(completeSegmentURL)
				if err != nil {
					return nil, err
				}
				localSegmentPath := fmt.Sprintf("%s/%s/segments/%s", v.VideoUID, resolution, segmentName)
				localSegmentPaths = append(localSegmentPaths, localSegmentPath)
				if !skipDownload {
					err = downloadFile(completeSegmentURL, localSegmentPath)
					if err != nil {
						return nil, err
					}
				}
			}

			if !skipDownload {
				// user-friendly progress output
				prog := int((float32(idx) / float32(totalSegments)) * 100)
				if prog%10 == 0 && prog != lastReportedProgress {
					msg := fmt.Sprintf("%d%% complete\n", prog)
					fmt.Fprint(writer, msg)
					writer.Flush()
					lastReportedProgress = prog
				}
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

// retrieveMasterPlaylist gets the master m3u8
func (v *Video) retrieveMasterPlaylist(url string) (*m3u8.MasterPlaylist, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	dataBuf := bytes.NewBuffer(body)
	playlist, _, err := m3u8.Decode(*dataBuf, false)
	if err != nil {
		return nil, err
	}

	masterPlaylist := playlist.(*m3u8.MasterPlaylist)
	return masterPlaylist, nil
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
func (v *Video) concatenateTSFiles(tsFiles []string, chosenResolution string) error {
	outputDir := fmt.Sprintf("%s/%s", v.VideoUID, chosenResolution)
	outputFilename := fmt.Sprintf("%s.mp4", v.VideoUID)
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
	cmd := exec.Command("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", tempFile.Name(), "-c", "copy", outputPath)

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("ffmpeg error: %w", err)
	}
	return nil
}

// printResolutionDownloadMenu lists available options to pull segments
// from an available resolution
func (v *Video) printResolutionDownloadMenu() (string, string, error) {
	var userOption int
	var manifestURLIdx []string
	var resolutionIdx []string

	reader := bufio.NewReader(os.Stdin)
	resolutionURLs := make(map[string]string)

	fmt.Printf("📋 Listing all available resolutions for video UID: %s\n\n", v.VideoUID)
	for idx, variant := range v.MasterPlaylist.Variants {
		manifestForResolution := fmt.Sprintf("%s/%s/manifest/%s", v.BaseURL, v.VideoUID, variant.URI)
		resolutionURLs[variant.Resolution] = manifestForResolution
		manifestURLIdx = append(manifestURLIdx, manifestForResolution)
		resolutionIdx = append(resolutionIdx, variant.Resolution)
		fmt.Printf("%d) %s\n", idx, variant.Resolution)
	}

	fmt.Printf("%d) 🚫 Exit\n", len(manifestURLIdx))
	fmt.Print("\n📼 Select resolution: ")
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
		fmt.Println("👋 Exiting Stream downloader")
		os.Exit(1)
	}

	chosenResolution := resolutionIdx[userOption]
	return resolutionURLs[chosenResolution], chosenResolution, nil
}

func (v *Video) renderOutputPaths(resolution string) {
	fmt.Println("Complete!")
	fmt.Println("---------------------------------------------")
	fmt.Printf("Video output:\n./%s/%s/%s.mp4\n\n", v.VideoUID, resolution, v.VideoUID)
	fmt.Printf("Segments output:\n./%s/%s/segments/\n", v.VideoUID, resolution)
	fmt.Printf("\nPlayback:\nffplay ./%s/%s/%s.mp4\n", v.VideoUID, resolution, v.VideoUID)
	fmt.Println("---------------------------------------------")
}
