package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"flag"
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
	"sync"

	"github.com/grafov/m3u8"
	"github.com/manifoldco/promptui"
	"github.com/schollz/progressbar/v3"
)

const (
	OPTION_DOWNLOAD            = "Download video and segments"
	OPTION_LIST_RESOLUTIONS    = "List available resolutions"
	OPTION_COUNT_SEGMENTS      = "Count number of segments"
	OPTION_UPLOAD_FILEPATH     = "Upload video from local file"
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
	manifestURLPointer := flag.String("manifestUrl", "", "URL to download video. (-- needs to be prepended)")
	absoluteOutputPathPointer := flag.String("outputPath", "", "path to output the audio and video segments along with the combined file. (-- needs to be prepended)")
	flag.Parse()

	manifestURL := *manifestURLPointer
	absoluteOutputPath := *absoluteOutputPathPointer

	if absoluteOutputPath != "" {
		if !fileExists(absoluteOutputPath) {
			log.Fatalf("Absolute path %s does not exist", absoluteOutputPath)
		}
	}

	if manifestURL == "" {
		fmt.Println("⚠️ WARNING: No HLS manifest was specified, so you will only be able to upload a video or add a manifest")
	}

	options := []string{
		OPTION_UPLOAD_FILEPATH,
		OPTION_CHANGE_MANIFEST_URL,
	}

	var prompt promptui.Select
	for {
		if manifestURL != "" {
			options = append(options, []string{
				OPTION_DOWNLOAD,
				OPTION_OUTPUT_MANIFEST_URL,
				OPTION_LIST_RESOLUTIONS,
				OPTION_COUNT_SEGMENTS,
				OPTION_EXIT,
			}...)
		}

		prompt = promptui.Select{
			Label: "Cloudflare Stream Downloader",
			Items: options,
		}

		_, result, err := prompt.Run()
		if err != nil {
			log.Fatal("Unable to process selection")
		}

		switch result {
		case OPTION_DOWNLOAD:
			initializeVideoDownloadProcess(manifestURL, absoluteOutputPath)
		case OPTION_OUTPUT_MANIFEST_URL:
			outputManifestURL(manifestURL)
		case OPTION_UPLOAD_FILEPATH:
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Enter absolute video file path: ")
			filename, err := reader.ReadString('\n')
			if err != nil {
				log.Fatal(err)
			}
			filename = filename[:len(filename)-1]
			initUpload(filename)
		case OPTION_LIST_RESOLUTIONS:
			listAvailableResolutions(manifestURL)
		case OPTION_COUNT_SEGMENTS:
			countTotalSegments(manifestURL, absoluteOutputPath)
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
}

// countTotalSegments will output the number of segments on a particular manifest
func countTotalSegments(manifestURL string, absoluteOutputPath string) {
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

	segmentPaths, err := video.downloadSegmentsFromManifest(chosenManifest, chosenResolution, true, false, absoluteOutputPath)
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
func initializeVideoDownloadProcess(manifestURL string, absoluteOutputPath string) {
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

	var storedPaths []string
	for _, media := range video.MasterPlaylist.Variants[0].Alternatives {
		if media.Type == "AUDIO" {
			manifestForResolution := fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, media.URI)
			segmentPaths, err := video.downloadSegmentsFromManifest(manifestForResolution, chosenResolution, false, true, absoluteOutputPath)
			if err != nil {
				log.Fatalf("there was a problem downloading the segments: %v", err)
			}

			storedPath, err := video.concatenateTSFiles(segmentPaths, chosenResolution, true)
			if err != nil {
				log.Fatalf("there was a problem concatenating the segments: %v", err)
			}
			storedPaths = append(storedPaths, storedPath)
		}
	}

	segmentPaths, err := video.downloadSegmentsFromManifest(chosenManifest, chosenResolution, false, false, absoluteOutputPath)
	if err != nil {
		log.Fatalf("there was a problem downloading the segments: %v", err)
	}

	storedPath, err := video.concatenateTSFiles(segmentPaths, chosenResolution, false)
	if err != nil {
		log.Fatalf("there was a problem concatenating the segments: %v", err)
	}
	storedPaths = append(storedPaths, storedPath)

	// merge potential audio and video files together with ffmpeg
	if len(storedPaths) >= 2 {
		fmt.Printf("🌱 audio and video are being merged...")
		video.mergeMP4FilesInDir(storedPaths)
	}
	video.renderOutputPaths(chosenResolution)
}

// downloadSegmentsFromManifest will download a complete video and individual segments
// from a particular manifest and returns the list of relative segment paths
func (v *Video) downloadSegmentsFromManifest(manifestURL, resolution string, skipDownload, isAudio bool, absoluteOutputPath string) ([]string, error) {
	if isAudio {
		fmt.Printf("🌱 Beginning audio download for [%s]\n", resolution)
	} else {
		fmt.Printf("🌱 Beginning video download for [%s]\n", resolution)
	}
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

	concurrencyLimit := 5
	sem := make(chan struct{}, concurrencyLimit)
	var wg sync.WaitGroup
	errChan := make(chan error, 1)

	localSegmentPaths := []string{}
	if listType == m3u8.MEDIA {
		mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
		if mediaPlaylist.Map != nil {
			segmentURL := mediaPlaylist.Map.URI
			for strings.HasPrefix(segmentURL, "../") {
				segmentURL = strings.TrimPrefix(segmentURL, "../")
			}
			completeSegmentURL := fmt.Sprintf("%s/%s", v.BaseURL, segmentURL)
			segmentName, err := getSegmentName(completeSegmentURL)
			if err != nil {
				return nil, err
			}
			var localSegmentPath string
			if isAudio {
				localSegmentPath = fmt.Sprintf("%s/%s/segments/audio_%s", absoluteOutputPath, resolution, segmentName)
			} else {
				localSegmentPath = fmt.Sprintf("%s/%s/segments/video_%s", absoluteOutputPath, resolution, segmentName)
			}
			localSegmentPaths = append(localSegmentPaths, localSegmentPath)
			if !skipDownload {
				err = downloadFile(completeSegmentURL, localSegmentPath)
				if err != nil {
					return nil, err
				}
			}
		}

		bar := progressbar.Default(int64(len(mediaPlaylist.Segments)))
		for _, segment := range mediaPlaylist.Segments {
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
				var localSegmentPath string
				if isAudio {
					localSegmentPath = fmt.Sprintf("%s/segments/audio_%s", resolution, segmentName)
				} else {
					localSegmentPath = fmt.Sprintf("%s/segments/video_%s", resolution, segmentName)
				}
				localSegmentPaths = append(localSegmentPaths, localSegmentPath)

				// parallelization for segment downloads
				if !skipDownload {
					sem <- struct{}{}
					wg.Add(1)
					go func() {
						defer func() {
							<-sem
							wg.Done()
						}()
						err := downloadFile(completeSegmentURL, localSegmentPath)
						if err != nil {
							select {
							case errChan <- err:
							default:
							}
						}
					}()
					bar.Add(1)
				}
			} else {
				bar.Add(1)
			}
		}
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		panic(err)
	}
	return localSegmentPaths, nil
}

// extractUIDAndPrefixURL will parse out the base URI for the customer as well
// as the UID for the video
func extractUIDAndPrefixURL(url string) (baseURL, uid string, err error) {
	regex := regexp.MustCompile(`^(.+)/(.+)/manifest/video.m3u8$`)
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
	segmentPattern := regexp.MustCompile(`(^seg_\d+|init)\.(ts|mp4)$`)
	if segmentPattern.MatchString(base) {
		return base, nil
	}
	return "", fmt.Errorf("segment name not found")
}

// concatenateTSFiles take all downloaded segments and concat into single, playable
// mp4 using ffmpeg
func (v *Video) concatenateTSFiles(filePaths []string, chosenResolution string, isAudio bool) (string, error) {
	var outputFilename string
	outputDir := chosenResolution
	outputFilename = "video.mp4"

	if isAudio {
		outputFilename = "audio.mp4"
	}

	currentDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for idx, file := range filePaths {
		updatedPath := path.Join(currentDirectory, file)
		filePaths[idx] = updatedPath
	}

	outputPath := path.Join(currentDirectory, outputDir, outputFilename)
	outputFile, err := os.Create(outputPath)
	if err != nil {
		log.Fatal(err)
	}
	defer outputFile.Close()

	for _, filePath := range filePaths {
		inputFile, err := os.Open(filePath)
		if err != nil {
			log.Fatal(err)
		}

		_, err = io.Copy(outputFile, inputFile)
		if err != nil {
			log.Fatal(err)
		}

		inputFile.Close()
	}
	return outputPath, nil
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

	userOption, err = strconv.Atoi(strings.TrimSpace(input[:len(input)-1]))
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
	fmt.Printf("Video output:\n./%s/\n\n", resolution)
	fmt.Println("---------------------------------------------")
}

func (v *Video) mergeMP4FilesInDir(filePaths []string) error {
	if len(filePaths) != 2 {
		return fmt.Errorf("expected 2 MP4 files, found %d", len(filePaths))
	}

	file, err := os.Open(filePaths[0])
	if err != nil {
		return err
	}
	defer file.Close()

	dirPath := filepath.Dir(file.Name())
	cmd := exec.Command("ffmpeg", "-i", filePaths[0], "-i", filePaths[1], "-c:v", "copy", "-c:a", "copy", fmt.Sprintf("%s/merged.mp4", dirPath))
	err = cmd.Run()
	if err != nil {
		return err
	}

	err = os.RemoveAll(filePaths[0])
	if err != nil {
		return err
	}
	err = os.RemoveAll(filePaths[1])
	if err != nil {
		return err
	}
	return nil
}

func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return !errors.Is(err, os.ErrNotExist)
}
