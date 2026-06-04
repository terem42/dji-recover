package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "dev"

func main() {
	refFile := flag.String("ref", "", "Reference (working) DJI MP4 for codec parameters `file` (required)")
	output := flag.String("o", "", "Output `file` (default: <input>_recovered.mp4)")
	rawH265 := flag.Bool("h265", false, "Output raw .h265 stream instead of MP4")
	fps := flag.Float64("fps", 59.94, "Frame rate for duration display and MP4 timing")
	gapOverride := flag.Int("gap", 0, "Override metadata gap in bytes (0 = auto-detect)")
	firstOffset := flag.Int64("offset", 0, "First video frame offset (0 = auto-detect)")
	verbose := flag.Bool("v", false, "Verbose output (print every large gap)")
	showVersion := flag.Bool("version", false, "Show version and exit")

	flag.Usage = func() {
		w := os.Stderr
		fmt.Fprintf(w, "DJI Video Recovery Tool v%s (c) terem42\n\n", version)
		fmt.Fprintf(w, "Recovers corrupted DJI Air Unit videos that lost their moov atom\n")
		fmt.Fprintf(w, "(file index) due to power loss or crash during recording.\n\n")

		fmt.Fprintf(w, "HOW IT WORKS\n\n")
		fmt.Fprintf(w, "  DJI records HEVC video interleaved with metadata (telemetry, debug)\n")
		fmt.Fprintf(w, "  into an MP4 container. When power is lost, the moov atom (which maps\n")
		fmt.Fprintf(w, "  frames to file offsets) is never written, leaving the raw data intact\n")
		fmt.Fprintf(w, "  but unplayable.\n\n")
		fmt.Fprintf(w, "  This tool performs a full forensic reconstruction:\n")
		fmt.Fprintf(w, "  1. Scans the raw file for HEVC video frames using NAL unit headers\n")
		fmt.Fprintf(w, "  2. Skips interleaved metadata using statistical gap analysis\n")
		fmt.Fprintf(w, "  3. Extracts DJMD telemetry chunks (gyro, GPS, exposure data)\n")
		fmt.Fprintf(w, "  4. Builds a complete MP4 with video + metadata tracks and moov atom\n\n")

		fmt.Fprintf(w, "OUTPUT FORMAT\n\n")
		fmt.Fprintf(w, "  By default, produces a ready-to-play MP4 file with two tracks:\n")
		fmt.Fprintf(w, "  - HEVC video track (hvc1) with codec parameters from reference\n")
		fmt.Fprintf(w, "  - DJMD metadata track with telemetry (gyro/accel/GPS) data\n\n")
		fmt.Fprintf(w, "  The output MP4 requires no post-processing — it opens directly in\n")
		fmt.Fprintf(w, "  VLC, DaVinci Resolve, Gyroflow, and other video tools.\n\n")
		fmt.Fprintf(w, "  Use -h265 to output a raw HEVC bitstream instead (requires ffmpeg\n")
		fmt.Fprintf(w, "  to mux into a playable container).\n\n")

		fmt.Fprintf(w, "WHY A REFERENCE FILE IS NEEDED\n\n")
		fmt.Fprintf(w, "  A working MP4 from the SAME camera and settings is required because\n")
		fmt.Fprintf(w, "  the corrupted file is missing critical information that can only be\n")
		fmt.Fprintf(w, "  obtained from a valid recording:\n\n")
		fmt.Fprintf(w, "  1. Codec parameters (VPS/SPS/PPS) — HEVC decoder initialization\n")
		fmt.Fprintf(w, "     data stored in the moov atom. Without these, no decoder can\n")
		fmt.Fprintf(w, "     interpret the raw video frames.\n\n")
		fmt.Fprintf(w, "  2. Metadata gap size — the byte distance between consecutive video\n")
		fmt.Fprintf(w, "     frames (occupied by telemetry/debug data). Auto-detected from\n")
		fmt.Fprintf(w, "     the reference file's sample size tables.\n\n")
		fmt.Fprintf(w, "  3. Frame size range — expected video NAL unit sizes, used to reject\n")
		fmt.Fprintf(w, "     false positives during scanning. Auto-detected from reference.\n\n")
		fmt.Fprintf(w, "  4. DJMD sample sizes — per-frame telemetry chunk sizes used to\n")
		fmt.Fprintf(w, "     extract metadata from gaps between video frames.\n\n")
		fmt.Fprintf(w, "  The reference file MUST use the same resolution, frame rate, and\n")
		fmt.Fprintf(w, "  codec settings. Any other recording from the same session or the\n")
		fmt.Fprintf(w, "  same camera configuration will work.\n\n")

		fmt.Fprintf(w, "USAGE\n\n")
		fmt.Fprintf(w, "  %s -ref <working.mp4> [flags] <corrupted.mp4>\n\n", os.Args[0])

		fmt.Fprintf(w, "FLAGS\n\n")
		flag.PrintDefaults()

		fmt.Fprintf(w, "\nEXAMPLES\n\n")
		fmt.Fprintf(w, "  # Recover to playable MP4 with DJMD telemetry (default)\n")
		fmt.Fprintf(w, "  %s -ref DJI_0022.MP4 DJI_0023.MP4\n\n", os.Args[0])
		fmt.Fprintf(w, "  # Recover to raw H.265 stream (for manual muxing with ffmpeg)\n")
		fmt.Fprintf(w, "  %s -ref DJI_0022.MP4 -h265 DJI_0023.MP4\n\n", os.Args[0])
		fmt.Fprintf(w, "  # Custom output path with verbose gap diagnostics\n")
		fmt.Fprintf(w, "  %s -ref DJI_0022.MP4 -v -o recovered.mp4 DJI_0023.MP4\n\n", os.Args[0])

		fmt.Fprintf(w, "GYROFLOW COMPATIBILITY\n\n")
		fmt.Fprintf(w, "  The recovered MP4 includes the DJMD telemetry track containing\n")
		fmt.Fprintf(w, "  gyroscope and accelerometer data. For Gyroflow stabilization:\n")
		fmt.Fprintf(w, "  - Record with Rocksteady/EIS OFF and FOV set to Wide\n")
		fmt.Fprintf(w, "  - Gyroflow auto-detects the DJMD track from the recovered MP4\n\n")

		fmt.Fprintf(w, "SUPPORTED CONFIGURATIONS\n\n")
		fmt.Fprintf(w, "  Resolution, frame rate, and bitrate are auto-detected from the\n")
		fmt.Fprintf(w, "  reference file. Tested with DJI O4 Pro at 1080p/60fps, but should\n")
		fmt.Fprintf(w, "  work with any DJI HEVC recording (2.7K, 4K, etc.) given a matching\n")
		fmt.Fprintf(w, "  reference file.\n")
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("dji-recover v%s\n", version)
		return
	}
	if flag.NArg() != 1 || *refFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	inputFile := flag.Arg(0)
	ext := ".mp4"
	if *rawH265 {
		ext = ".h265"
	}
	if *output == "" {
		*output = strings.TrimSuffix(inputFile, filepath.Ext(inputFile)) + "_recovered" + ext
	}

	fmt.Printf("DJI Video Recovery Tool v%s (c) terem42\n", version)
	fmt.Println(strings.Repeat("─", 50))

	// ── Parse reference ──
	fmt.Printf("\nReference: %s\n", filepath.Base(*refFile))
	refData, err := os.ReadFile(*refFile)
	if err != nil {
		fatal("reading reference: %v", err)
	}
	ref, err := parseReference(refData)
	if err != nil {
		fatal("parsing reference: %v", err)
	}
	refData = nil

	if *gapOverride > 0 {
		ref.GapMedian = *gapOverride
	}

	fmt.Printf("  Codec params: ")
	for i, p := range ref.Params {
		names := map[byte]string{32: "VPS", 33: "SPS", 34: "PPS"}
		n := names[p.Type]
		if n == "" {
			n = fmt.Sprintf("T%d", p.Type)
		}
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%s(%db)", n, len(p.Data))
	}
	fmt.Println()
	fmt.Printf("  Metadata gap: %d bytes\n", ref.GapMedian)
	fmt.Printf("  NAL size range: %d - %d bytes\n", ref.NALMin, ref.NALMax)
	fmt.Printf("  Video: %dx%d\n", ref.Width, ref.Height)
	if ref.DJMDStsd != nil {
		fmt.Printf("  DJMD track: yes (%d reference sizes)\n", len(ref.DJMDSizes))
	} else {
		fmt.Println("  DJMD track: not found in reference")
	}

	// ── Load corrupted ──
	fmt.Printf("\nInput: %s\n", filepath.Base(inputFile))
	corData, err := os.ReadFile(inputFile)
	if err != nil {
		fatal("reading input: %v", err)
	}
	fmt.Printf("  Size: %.2f GB (%d bytes)\n", float64(len(corData))/(1024*1024*1024), len(corData))

	var startOff int
	if *firstOffset > 0 {
		startOff = int(*firstOffset)
		fmt.Printf("  First frame: 0x%x (user-specified)\n", startOff)
	} else {
		startOff = findFirstIDR(corData, ref)
		if startOff < 0 {
			fatal("could not auto-detect first video frame")
		}
		fmt.Printf("  First frame: 0x%x (auto-detected)\n", startOff)
	}

	// ── Scan frames ──
	fmt.Printf("\nScanning frames...\n")
	scanStart := time.Now()
	frames, stats := scanFrames(corData, ref, startOff, *verbose)
	scanElapsed := time.Since(scanStart)

	if stats.Frames == 0 {
		fatal("no frames recovered")
	}

	// ── Write output ──
	fmt.Printf("\nWriting: %s\n", *output)
	writeStart := time.Now()

	if *rawH265 {
		if err := writeH265(*output, corData, frames, ref); err != nil {
			fatal("writing h265: %v", err)
		}
	} else {
		if err := writeMp4(*output, corData, frames, ref); err != nil {
			fatal("writing mp4: %v", err)
		}
	}
	writeElapsed := time.Since(writeStart)

	// ── Summary ──
	dur := float64(stats.Frames) / *fps
	fmt.Printf("\n%s\n", strings.Repeat("─", 50))
	fmt.Printf("Recovery complete (scan: %s, write: %s)\n\n",
		scanElapsed.Round(time.Millisecond), writeElapsed.Round(time.Millisecond))
	fmt.Printf("  Frames:    %d (%d keyframes)\n", stats.Frames, stats.Keyframes)
	fmt.Printf("  Duration:  %d:%02d (%.1fs @ %.2f fps)\n",
		int(dur)/60, int(dur)%60, dur, *fps)
	fmt.Printf("  Video:     %.2f GB\n", float64(stats.VideoBytes)/(1024*1024*1024))
	outInfo, _ := os.Stat(*output)
	if outInfo != nil {
		fmt.Printf("  Output:    %.2f GB\n", float64(outInfo.Size())/(1024*1024*1024))
	}
	fmt.Printf("  Large gaps: %d\n", stats.LargeGaps)
	fmt.Printf("  IDR restarts: %d\n", stats.IDRRestarts)
	if stats.GapMedian > 0 {
		fmt.Printf("  Gap stats: median=%d min=%d max=%d\n",
			stats.GapMedian, stats.GapMin, stats.GapMax)
	}

	djmdCount := 0
	for _, f := range frames {
		if f.DJMDSize > 0 {
			djmdCount++
		}
	}
	if djmdCount > 0 && !*rawH265 {
		fmt.Printf("  DJMD:      %d metadata chunks included\n", djmdCount)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\n✗ Error: "+format+"\n", args...)
	os.Exit(1)
}
