package audio

import (
	"errors"
	"io"
	"os"
	"sort"
)

// TimelineClip places one PCM16 WAV on a wall-clock timeline. StartMS is
// relative to the beginning of the merged meeting.
type TimelineClip struct {
	Path    string
	StartMS int64
}

// MergePCM16WAVTimeline streams compatible WAV clips into one file, inserting
// silence for gaps. If reconnecting recordings overlap, the already written
// earlier clip wins and the overlapping prefix of the later clip is skipped.
func MergePCM16WAVTimeline(clips []TimelineClip, outputPath string) error {
	clips = append([]TimelineClip(nil), clips...)
	sort.SliceStable(clips, func(i, j int) bool { return clips[i].StartMS < clips[j].StartMS })
	if len(clips) == 0 {
		return errors.New("no WAV clips to merge")
	}
	var writer *WAVWriter
	var sampleRate, channels int
	var cursorFrames int64
	for _, clip := range clips {
		input, err := os.Open(clip.Path)
		if err != nil {
			if writer != nil {
				_ = writer.Close()
			}
			return err
		}
		info, err := ReadWAVInfo(input)
		if err != nil {
			_ = input.Close()
			if writer != nil {
				_ = writer.Close()
			}
			return err
		}
		if info.BitsPerSample != 16 || info.SampleRate <= 0 || info.Channels <= 0 {
			_ = input.Close()
			if writer != nil {
				_ = writer.Close()
			}
			return errors.New("timeline merge supports PCM16 WAV only")
		}
		if writer == nil {
			sampleRate, channels = info.SampleRate, info.Channels
			writer, err = NewWAVWriter(outputPath, sampleRate, channels)
			if err != nil {
				_ = input.Close()
				return err
			}
		} else if info.SampleRate != sampleRate || info.Channels != channels {
			_ = input.Close()
			_ = writer.Close()
			return errors.New("timeline WAV clips are incompatible")
		}
		bytesPerFrame := int64(channels * 2)
		startFrame := clip.StartMS * int64(sampleRate) / 1000
		if startFrame > cursorFrames {
			if err := writeSilentFrames(writer, startFrame-cursorFrames, bytesPerFrame); err != nil {
				_ = input.Close()
				_ = writer.Close()
				return err
			}
			cursorFrames = startFrame
		}
		skipFrames := cursorFrames - startFrame
		if skipFrames < 0 {
			skipFrames = 0
		}
		skipBytes := skipFrames * bytesPerFrame
		if skipBytes >= info.DataSize {
			_ = input.Close()
			continue
		}
		if _, err := input.Seek(info.DataOffset+skipBytes, io.SeekStart); err != nil {
			_ = input.Close()
			_ = writer.Close()
			return err
		}
		remaining := info.DataSize - skipBytes
		buf := make([]byte, 64*1024)
		for remaining > 0 {
			want := int64(len(buf))
			if remaining < want {
				want = remaining
			}
			want -= want % bytesPerFrame
			if want == 0 {
				break
			}
			n, readErr := io.ReadFull(input, buf[:want])
			if n > 0 {
				if err := writer.WritePCM16(buf[:n]); err != nil {
					_ = input.Close()
					_ = writer.Close()
					return err
				}
				remaining -= int64(n)
				cursorFrames += int64(n) / bytesPerFrame
			}
			if readErr != nil {
				_ = input.Close()
				_ = writer.Close()
				return readErr
			}
		}
		_ = input.Close()
	}
	if writer == nil {
		return errors.New("no readable WAV clips")
	}
	return writer.Close()
}

func writeSilentFrames(writer *WAVWriter, frames, bytesPerFrame int64) error {
	zeros := make([]byte, 64*1024)
	remaining := frames * bytesPerFrame
	for remaining > 0 {
		n := int64(len(zeros))
		if remaining < n {
			n = remaining
		}
		n -= n % bytesPerFrame
		if n == 0 {
			break
		}
		if err := writer.WritePCM16(zeros[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}
