# dji-recover

Recovers corrupted DJI Air Unit video files that lost their moov atom
(file index) due to power loss or crash during recording.

Produces a ready-to-play MP4 with both the HEVC video track and the DJMD
telemetry track (gyroscope, accelerometer, GPS), with no external
dependencies. Single static binary, no ffmpeg required.

## Build

    go build -o dji-recover

## Usage

    ./dji-recover -ref <working.mp4> <corrupted.mp4>

A reference file from the same camera and recording settings is required.
The tool auto-detects codec parameters, metadata gap size, frame size
range, and telemetry chunk sizes from it.

### Options

    -ref file     Reference (working) DJI MP4 (required)
    -o file       Output file (default: <input>_recovered.mp4)
    -h265         Output raw H.265 stream instead of MP4
    -fps float    Frame rate (default 59.94)
    -gap int      Override metadata gap in bytes (0 = auto-detect)
    -offset int   First video frame offset (0 = auto-detect)
    -v            Verbose output (print every large gap)

### Examples

    # Recover to playable MP4 with telemetry
    ./dji-recover -ref DJI_0022.MP4 DJI_0023.MP4

    # Raw H.265 stream for manual processing
    ./dji-recover -ref DJI_0022.MP4 -h265 DJI_0023.MP4

## How it works

DJI cameras record HEVC video interleaved with metadata (telemetry and
debug data) into an MP4 container. The moov atom, which maps frame
positions to file offsets, is written last when recording stops. If power
is lost mid-flight, the moov is never written, leaving the raw data
intact but unplayable.

This tool performs forensic reconstruction:

1. Scans the corrupted file for HEVC NAL units, validating each frame
   by header structure, size range, and temporal layer ID.
2. Tracks inter-frame distances using adaptive gap analysis to skip
   interleaved metadata blocks.
3. Handles large metadata-induced jumps (up to 2 MB) with chain-verified
   re-synchronization to avoid false positives.
4. Extracts DJMD telemetry chunks from the gaps between video frames.
5. Builds a complete MP4 container with proper sample tables, chunk
   offsets, and codec initialization data (VPS/SPS/PPS from reference).

## Output

The recovered MP4 contains two tracks:

- **Video** -- HEVC (hvc1) with codec parameters copied from reference
- **DJMD** -- DJI telemetry (gyro, accelerometer, GPS, exposure)

The file opens directly in VLC, DaVinci Resolve, Gyroflow, and other
standard video tools.

## Gyroflow compatibility

The DJMD track contains gyroscope and accelerometer data that Gyroflow
can use for post-flight stabilization. Requirements:

- Record with Rocksteady/EIS disabled and FOV set to Wide
- Gyroflow auto-detects the DJMD track from the recovered MP4

## Supported configurations

Resolution, frame rate, and bitrate are auto-detected from the reference
file. Tested with DJI O4 Pro at 1080p/60fps. Should work with any DJI
HEVC recording (2.7K, 4K, etc.) given a matching reference file.

## License

MIT -- terem42
