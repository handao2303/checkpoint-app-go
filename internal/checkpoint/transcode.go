package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Video transcoding limits. The browser only validates these for UX; the server
// is authoritative because many browsers cannot produce MP4 client-side.
const (
	// maxVideoInputBytes caps the raw upload accepted for transcoding. Generous
	// because source files (MOV/WebM/high-bitrate MP4) are larger than the
	// compressed result.
	maxVideoInputBytes = 60 << 20 // 60 MiB
	// maxVideoOutputBytes caps the transcoded MP4. A 10s 720p H.264 clip is well
	// under this; anything larger is rejected so feeds stay light.
	maxVideoOutputBytes = 12 << 20 // 12 MiB
	// maxVideoDurationSeconds truncates clips during transcode.
	maxVideoDurationSeconds = 10
)

// ErrTranscodeUnavailable is returned when the ffmpeg binary is not installed.
var ErrTranscodeUnavailable = errors.New("video transcoding unavailable")

// transcodeToMP4 converts arbitrary video bytes into a web-friendly MP4
// (H.264 baseline + AAC, longest edge ≤720, faststart) using ffmpeg. Rotation
// metadata is applied automatically. The clip is truncated to
// maxVideoDurationSeconds. Returns ErrTranscodeUnavailable if ffmpeg is absent.
func transcodeToMP4(ctx context.Context, input []byte) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, ErrTranscodeUnavailable
	}

	in, err := os.CreateTemp("", "cp-video-in-*")
	if err != nil {
		return nil, fmt.Errorf("create temp input: %w", err)
	}
	defer func() { _ = os.Remove(in.Name()) }()
	if _, err := in.Write(input); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("write temp input: %w", err)
	}
	if err := in.Close(); err != nil {
		return nil, fmt.Errorf("close temp input: %w", err)
	}

	// ffmpeg needs a seekable output path for +faststart, so write to a temp
	// file rather than stdout. The .mp4 extension picks the muxer.
	out, err := os.CreateTemp("", "cp-video-out-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create temp output: %w", err)
	}
	outName := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outName) }()

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", in.Name(),
		"-t", fmt.Sprintf("%d", maxVideoDurationSeconds),
		// Bound both dimensions by 720 keeping aspect ratio; H.264/yuv420p needs
		// even dimensions, so force divisibility by 2.
		"-vf", "scale='min(720,iw)':'min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.1",
		"-preset", "veryfast", "-crf", "28", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k",
		"-movflags", "+faststart",
		outName,
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg transcode: %w: %s", err, stderr.String())
	}

	result, err := os.ReadFile(outName)
	if err != nil {
		return nil, fmt.Errorf("read transcoded output: %w", err)
	}
	if len(result) == 0 {
		return nil, errors.New("transcode produced empty output")
	}
	return result, nil
}
