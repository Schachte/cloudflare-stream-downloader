package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

// Segment represents a single audio or video segment to download
type Segment struct {
	URL       string
	LocalPath string
	Duration  float64
	IsAudio   bool
	Index     int
}

// DownloadConfig holds configuration for the interleaved downloader
type DownloadConfig struct {
	Concurrency     int
	RealtimeMode    bool
	RealtimeFactor  float64
	WorkDir         string
	OutputFile      string
	Resolution      string
}

func main() {
	manifestURLPointer := flag.String("manifestUrl", "", "URL to download video. (-- needs to be prepended)")
	absoluteOutputPathPointer := flag.String("outputPath", "", "path to output the audio and video segments along with the combined file. (-- needs to be prepended)")
	downloadFlag := flag.Bool("download", false, "skip UI and immediately download the best resolution")
	listFlag := flag.Bool("list", false, "list available resolutions and exit")
	resolutionFlag := flag.String("resolution", "", "specific resolution to download (e.g. 1920x1080)")
	concurrencyFlag := flag.Int("concurrency", 5, "number of concurrent segment downloads")
	realtimeFlag := flag.Float64("realtime", 0, "simulate real-time playback speed (e.g., 1.0 for real-time, 0.9 for 90%)")
	workDirFlag := flag.String("workDir", "", "working directory for temp files (defaults to OS temp dir)")
	outputFlag := flag.String("output", "", "output file path (defaults to ./<resolution>/output.mp4)")
	flag.Parse()

	manifestURL := *manifestURLPointer
	absoluteOutputPath := *absoluteOutputPathPointer

	if absoluteOutputPath != "" {
		if !fileExists(absoluteOutputPath) {
			log.Fatalf("Absolute path %s does not exist", absoluteOutputPath)
		}
	}

	if *listFlag {
		if manifestURL == "" {
			log.Fatal("--list requires --manifestUrl to be set")
		}
		listResolutionsNonInteractive(manifestURL)
		return
	}

	if *downloadFlag {
		if manifestURL == "" {
			log.Fatal("--download requires --manifestUrl to be set")
		}
		workDir := *workDirFlag
		if workDir == "" {
			var err error
			workDir, err = os.MkdirTemp("", "stream-download-*")
			if err != nil {
				log.Fatalf("failed to create temp directory: %v", err)
			}
		}
		config := DownloadConfig{
			Concurrency:    *concurrencyFlag,
			RealtimeMode:   *realtimeFlag > 0,
			RealtimeFactor: *realtimeFlag,
			WorkDir:        workDir,
			OutputFile:     *outputFlag,
			Resolution:     *resolutionFlag,
		}
		downloadInterleaved(manifestURL, config)
		return
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

// listResolutionsNonInteractive prints available resolutions one per line
func listResolutionsNonInteractive(manifestURL string) {
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

	for _, variant := range masterPlaylist.Variants {
		fmt.Println(variant.Resolution)
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

// initializeVideoDownloadProcessNonInteractive downloads without prompts, selecting best or specified resolution
func initializeVideoDownloadProcessNonInteractive(manifestURL string, absoluteOutputPath string, targetResolution string) {
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

	// Select resolution: use specified, or pick the best (last variant typically has highest quality)
	var chosenManifest, chosenResolution string
	if targetResolution != "" {
		found := false
		for _, variant := range video.MasterPlaylist.Variants {
			if variant.Resolution == targetResolution {
				chosenResolution = variant.Resolution
				chosenManifest = fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, variant.URI)
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("resolution %s not found. Available resolutions:", targetResolution)
		}
	} else {
		// Pick the best resolution (last one in the list, typically highest bandwidth)
		bestVariant := video.MasterPlaylist.Variants[len(video.MasterPlaylist.Variants)-1]
		chosenResolution = bestVariant.Resolution
		chosenManifest = fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, bestVariant.URI)
	}

	fmt.Printf("📺 Selected resolution: %s\n", chosenResolution)

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

	if len(storedPaths) >= 2 {
		fmt.Printf("🌱 audio and video are being merged...")
		video.mergeMP4FilesInDir(storedPaths)
	}
	video.renderOutputPaths(chosenResolution)
}

// downloadInterleaved downloads audio and video segments interleaved by percentage progress
func downloadInterleaved(manifestURL string, config DownloadConfig) {
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

	// Select resolution
	var chosenManifest, chosenResolution string
	if config.Resolution != "" {
		found := false
		for _, variant := range video.MasterPlaylist.Variants {
			if variant.Resolution == config.Resolution {
				chosenResolution = variant.Resolution
				chosenManifest = fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, variant.URI)
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("resolution %s not found", config.Resolution)
		}
	} else {
		bestVariant := video.MasterPlaylist.Variants[len(video.MasterPlaylist.Variants)-1]
		chosenResolution = bestVariant.Resolution
		chosenManifest = fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, bestVariant.URI)
	}

	fmt.Printf("📺 Selected resolution: %s\n", chosenResolution)

	// Get audio manifest URL
	var audioManifestURL string
	for _, media := range video.MasterPlaylist.Variants[0].Alternatives {
		if media.Type == "AUDIO" {
			audioManifestURL = fmt.Sprintf("%s/%s/manifest/%s", video.BaseURL, video.VideoUID, media.URI)
			break
		}
	}

	// Parse both manifests to get segment lists
	audioSegments, audioDuration, err := parseManifestSegments(audioManifestURL, video.BaseURL, chosenResolution, config.WorkDir, true)
	if err != nil {
		log.Fatalf("failed to parse audio manifest: %v", err)
	}

	videoSegments, videoDuration, err := parseManifestSegments(chosenManifest, video.BaseURL, chosenResolution, config.WorkDir, false)
	if err != nil {
		log.Fatalf("failed to parse video manifest: %v", err)
	}

	totalDuration := audioDuration
	if videoDuration > totalDuration {
		totalDuration = videoDuration
	}

	fmt.Printf("🎬 Video duration: %.1fs | Audio segments: %d | Video segments: %d\n",
		totalDuration, len(audioSegments), len(videoSegments))

	if config.RealtimeMode {
		fmt.Printf("⏱️  Real-time mode: %.0f%% speed (download will take ~%.1fs)\n",
			config.RealtimeFactor*100, totalDuration/config.RealtimeFactor)
	}

	// Build interleaved download queue based on percentage progress
	queue := buildInterleavedQueue(audioSegments, videoSegments)

	// Download with progress bar
	err = downloadQueue(queue, config, totalDuration)
	if err != nil {
		fmt.Printf("❌ Download failed: %v\n", err)
		fmt.Println("---------------------------------------------")
		fmt.Printf("Working files preserved at: %s\n", config.WorkDir)
		fmt.Println("---------------------------------------------")
		os.Exit(1)
	}

	// Merge using ffmpeg concat demuxer
	outputFile := config.OutputFile
	if outputFile == "" {
		outputFile = filepath.Join(chosenResolution, "output.mp4")
	}
	err = mergeWithFFmpegConcat(audioSegments, videoSegments, outputFile, config.WorkDir, totalDuration)
	if err != nil {
		fmt.Printf("❌ Merge failed: %v\n", err)
		fmt.Println("---------------------------------------------")
		fmt.Printf("Working files preserved at: %s\n", config.WorkDir)
		fmt.Println("---------------------------------------------")
		os.Exit(1)
	}

	// Clean up working directory
	fmt.Println("🧹 Cleaning up working directory...")
	if err := os.RemoveAll(config.WorkDir); err != nil {
		fmt.Printf("⚠️  Warning: failed to clean up working directory: %v\n", err)
	}

	fmt.Println("✅ Complete!")
	fmt.Println("---------------------------------------------")
	fmt.Printf("Video output: %s\n", outputFile)
	fmt.Println("---------------------------------------------")
}

// parseManifestSegments fetches a manifest and returns the segment list with durations
func parseManifestSegments(manifestURL, baseURL, resolution, outputPath string, isAudio bool) ([]Segment, float64, error) {
	resp, err := http.Get(manifestURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	dataBuf := bytes.NewBuffer(body)
	playlist, listType, err := m3u8.Decode(*dataBuf, false)
	if err != nil {
		return nil, 0, err
	}

	if listType != m3u8.MEDIA {
		return nil, 0, fmt.Errorf("expected media playlist, got master")
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
	var segments []Segment
	var totalDuration float64
	index := 0

	// Handle init segment if present
	if mediaPlaylist.Map != nil {
		segmentURL := mediaPlaylist.Map.URI
		for strings.HasPrefix(segmentURL, "../") {
			segmentURL = strings.TrimPrefix(segmentURL, "../")
		}
		completeURL := fmt.Sprintf("%s/%s", baseURL, segmentURL)
		segmentName, err := getSegmentName(completeURL)
		if err == nil {
			prefix := "video_"
			if isAudio {
				prefix = "audio_"
			}
			localPath := filepath.Join(outputPath, resolution, "segments", prefix+segmentName)
			segments = append(segments, Segment{
				URL:       completeURL,
				LocalPath: localPath,
				Duration:  0,
				IsAudio:   isAudio,
				Index:     index,
			})
			index++
		}
	}

	for _, seg := range mediaPlaylist.Segments {
		if seg == nil {
			continue
		}
		segmentURL := seg.URI
		for strings.HasPrefix(segmentURL, "../") {
			segmentURL = strings.TrimPrefix(segmentURL, "../")
		}
		completeURL := fmt.Sprintf("%s/%s", baseURL, segmentURL)
		segmentName, err := getSegmentName(completeURL)
		if err != nil {
			continue
		}
		prefix := "video_"
		if isAudio {
			prefix = "audio_"
		}
		localPath := filepath.Join(outputPath, resolution, "segments", prefix+segmentName)
		segments = append(segments, Segment{
			URL:       completeURL,
			LocalPath: localPath,
			Duration:  seg.Duration,
			IsAudio:   isAudio,
			Index:     index,
		})
		totalDuration += seg.Duration
		index++
	}

	return segments, totalDuration, nil
}

// buildInterleavedQueue creates a download queue interleaved by percentage progress
func buildInterleavedQueue(audioSegments, videoSegments []Segment) []Segment {
	type indexedSegment struct {
		segment    Segment
		percentage float64
	}

	var indexed []indexedSegment
	audioCount := len(audioSegments)
	videoCount := len(videoSegments)

	for i, seg := range audioSegments {
		pct := float64(i) / float64(audioCount)
		indexed = append(indexed, indexedSegment{seg, pct})
	}
	for i, seg := range videoSegments {
		pct := float64(i) / float64(videoCount)
		indexed = append(indexed, indexedSegment{seg, pct})
	}

	// Sort by percentage, with audio before video at same percentage
	sort.SliceStable(indexed, func(i, j int) bool {
		if indexed[i].percentage != indexed[j].percentage {
			return indexed[i].percentage < indexed[j].percentage
		}
		return indexed[i].segment.IsAudio && !indexed[j].segment.IsAudio
	})

	queue := make([]Segment, len(indexed))
	for i, item := range indexed {
		queue[i] = item.segment
	}
	return queue
}

// downloadQueue downloads segments with concurrency control and optional real-time pacing
func downloadQueue(queue []Segment, config DownloadConfig, totalDuration float64) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := make(chan struct{}, config.Concurrency)
	var wg sync.WaitGroup
	errChan := make(chan error, 1)

	totalSegments := len(queue)
	bar := progressbar.NewOptions(totalSegments,
		progressbar.OptionSetDescription("Downloading"),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(40),
	)

	var audioDownloaded, videoDownloaded int
	var mu sync.Mutex
	startTime := time.Now()
	var cumulativeDuration float64

downloadLoop:
	for _, seg := range queue {
		select {
		case <-ctx.Done():
			break downloadLoop
		default:
		}

		seg := seg
		cumulativeDuration += seg.Duration

		// Real-time pacing: wait if we're ahead of schedule
		if config.RealtimeMode && cumulativeDuration > 0 {
			targetTime := time.Duration(cumulativeDuration/config.RealtimeFactor) * time.Second
			elapsed := time.Since(startTime)
			if elapsed < targetTime {
				time.Sleep(targetTime - elapsed)
			}
		}

		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()

			select {
			case <-ctx.Done():
				return
			default:
			}

			err := downloadFile(seg.URL, seg.LocalPath)
			if err != nil {
				select {
				case errChan <- fmt.Errorf("failed to download %s: %w", seg.URL, err):
					cancel()
				default:
				}
				return
			}

			mu.Lock()
			if seg.IsAudio {
				audioDownloaded++
			} else {
				videoDownloaded++
			}
			bar.Describe(fmt.Sprintf("A:%d V:%d", audioDownloaded, videoDownloaded))
			bar.Add(1)
			mu.Unlock()
		}()
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return err
	}

	fmt.Println()
	return nil
}

// mergeWithFFmpegConcat uses ffmpeg concat demuxer to merge segments directly
func mergeWithFFmpegConcat(audioSegments, videoSegments []Segment, outputFile, workDir string, totalDuration float64) error {
	fmt.Println("🔧 Merging with ffmpeg...")

	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Create concat file for audio
	audioListFile := filepath.Join(workDir, "audio_list.txt")
	audioList, err := os.Create(audioListFile)
	if err != nil {
		return err
	}
	for _, seg := range audioSegments {
		absPath, _ := filepath.Abs(seg.LocalPath)
		fmt.Fprintf(audioList, "file '%s'\n", absPath)
	}
	audioList.Close()

	// Create concat file for video
	videoListFile := filepath.Join(workDir, "video_list.txt")
	videoList, err := os.Create(videoListFile)
	if err != nil {
		return err
	}
	for _, seg := range videoSegments {
		absPath, _ := filepath.Abs(seg.LocalPath)
		fmt.Fprintf(videoList, "file '%s'\n", absPath)
	}
	videoList.Close()

	// Run ffmpeg to concat and merge in one step with progress output
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0", "-i", audioListFile,
		"-f", "concat", "-safe", "0", "-i", videoListFile,
		"-c:v", "copy", "-c:a", "copy",
		"-map", "1:v:0", "-map", "0:a:0",
		"-progress", "pipe:1",
		outputFile)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return err
	}

	// Parse progress output
	bar := progressbar.NewOptions(100,
		progressbar.OptionSetDescription("Merging"),
		progressbar.OptionSetWidth(40),
		progressbar.OptionShowBytes(false),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionClearOnFinish(),
	)

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "out_time_ms=") {
			timeStr := strings.TrimPrefix(line, "out_time_ms=")
			if timeUs, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
				currentSec := float64(timeUs) / 1000000.0
				percent := int((currentSec / totalDuration) * 100)
				if percent > 100 {
					percent = 100
				}
				bar.Set(percent)
			}
		}
	}

	err = cmd.Wait()
	bar.Finish()

	if err != nil {
		return fmt.Errorf("ffmpeg failed: %v", err)
	}

	// Clean up list files
	os.Remove(audioListFile)
	os.Remove(videoListFile)

	return nil
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

	body, err := io.ReadAll(resp.Body)
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
	regex := regexp.MustCompile(`^(.+)/(.+)/manifest/.+\.m3u8$`)
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

	body, err := io.ReadAll(resp.Body)
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
