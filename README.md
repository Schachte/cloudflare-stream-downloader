# Cloudflare Stream Video Downloader

## Supports
- Downloading videos of a user-selected resolution
- Downloading individual segments of a user-selected resolution

## Usage
```sh
./stream-download <HLS_MANIFEST_URL>
```

For building the binary, see section below on `Builds & Releases` or [download latest release here.](https://github.com/Schachte/cloudflare-stream-downloader/releases)

You can grab the HLS manifest from the Cloudflare Dash as shown in the image below:

![](./assets/dashboard.png)

## Example Output
```
Listing all available resolutions for video UID: 123456

0) 854x480
1) 1920x1080
2) 1280x720
3) 640x360
4) 426x240
5) Exit

---------------------------------------------

Select which resolution you'd like to download: 3

---------------------------------------------
Beginning download for: 640x360
Starting download...
5% complete
10% complete
...
100% complete

---------------------------------------------
Complete!
---------------------------------------------
Output [video] will be in the directory
./123456/640x360/123456.mp4

Output [segments] will be in the directory
./123456/640x360/segments/
```

## Builds & Releases

### Releases 

[Download latest release here.](https://github.com/Schachte/cloudflare-stream-downloader/releases)

or 

You can also run the latest builds for all operating systems by running: `make all`. 

## Playback

```
ffplay <UID>/<RESOLUTION>/<UID>.mp4
```