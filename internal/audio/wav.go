// Package audio provides PCM16 WAV reading, writing, mixing and import helpers.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// WAVWriter streams PCM16 samples into a WAV file.
type WAVWriter struct {
	mu         sync.Mutex
	f          *os.File
	path       string
	sampleRate int
	channels   int
	dataBytes  uint32
	closed     bool
}

// SegmentRMSPCM16WAV returns the normalized RMS level (0..1) for a time
// interval. It is used as a conservative fallback when whisper-cli has no VAD:
// text decoded from digital silence is discarded instead of becoming a fake
// subtitle credit or a person's name.
func SegmentRMSPCM16WAV(f *os.File, info WAVInfo, startMS, endMS int64) (float64, error) {
	if info.BitsPerSample != 16 || info.SampleRate <= 0 || info.Channels <= 0 {
		return 0, errors.New("RMS supports PCM16 WAV only")
	}
	if startMS < 0 {
		startMS = 0
	}
	if endMS <= startMS {
		return 0, nil
	}
	bytesPerFrame := int64(info.Channels * 2)
	startFrame := startMS * int64(info.SampleRate) / 1000
	endFrame := endMS * int64(info.SampleRate) / 1000
	startByte := startFrame * bytesPerFrame
	endByte := endFrame * bytesPerFrame
	if startByte > info.DataSize {
		return 0, nil
	}
	if endByte > info.DataSize {
		endByte = info.DataSize
	}
	if endByte <= startByte {
		return 0, nil
	}
	if _, err := f.Seek(info.DataOffset+startByte, io.SeekStart); err != nil {
		return 0, err
	}
	reader := io.LimitReader(f, endByte-startByte)
	buf := make([]byte, 32*1024)
	var sum float64
	var count int64
	for {
		n, err := reader.Read(buf)
		for i := 0; i+1 < n; i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(buf[i:i+2]))) / 32768.0
			sum += v * v
			count++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if count == 0 {
		return 0, nil
	}
	return math.Sqrt(sum / float64(count)), nil
}

// NewWAVWriter creates a WAV file whose data chunk size is finalized on Close.
func NewWAVWriter(path string, sampleRate, channels int) (*WAVWriter, error) {
	if sampleRate <= 0 || channels <= 0 {
		return nil, errors.New("invalid WAV parameters")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	w := &WAVWriter{f: f, path: path, sampleRate: sampleRate, channels: channels}
	if err := w.writeHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// WritePCM16 appends little-endian PCM16 samples.
func (w *WAVWriter) WritePCM16(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("WAV writer is closed")
	}
	if len(data)%2 != 0 {
		return errors.New("PCM16 chunk has odd size")
	}
	n, err := w.f.Write(data)
	w.dataBytes += uint32(n)
	return err
}

// Close finalizes the RIFF header and closes the file.
func (w *WAVWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := w.writeHeader(); err != nil {
		return err
	}
	return w.f.Close()
}

func (w *WAVWriter) writeHeader() error {
	byteRate := uint32(w.sampleRate * w.channels * 2)
	blockAlign := uint16(w.channels * 2)
	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36+w.dataBytes)
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1)
	binary.LittleEndian.PutUint16(h[22:24], uint16(w.channels))
	binary.LittleEndian.PutUint32(h[24:28], uint32(w.sampleRate))
	binary.LittleEndian.PutUint32(h[28:32], byteRate)
	binary.LittleEndian.PutUint16(h[32:34], blockAlign)
	binary.LittleEndian.PutUint16(h[34:36], 16)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], w.dataBytes)
	_, err := w.f.Write(h)
	return err
}

// WAVInfo describes the format and payload size of a WAV file.
type WAVInfo struct {
	SampleRate, Channels, BitsPerSample int
	DataOffset, DataSize                int64
}

// PCM16WAVDurationMS returns the exact duration represented by the PCM data.
// LocalMeetAssist uses it to split long recordings without decoding the whole file
// into memory.
func PCM16WAVDurationMS(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := ReadWAVInfo(f)
	if err != nil {
		return 0, err
	}
	if info.BitsPerSample != 16 || info.SampleRate <= 0 || info.Channels <= 0 {
		return 0, errors.New("duration supports PCM16 WAV only")
	}
	bytesPerFrame := int64(info.Channels * 2)
	frames := info.DataSize / bytesPerFrame
	return frames * 1000 / int64(info.SampleRate), nil
}

// SlicePCM16WAV copies a time range to a standalone WAV file. The operation is
// streaming and therefore keeps memory usage constant even for multi-hour
// meetings.
func SlicePCM16WAV(sourcePath, outputPath string, startMS, endMS int64) error {
	if startMS < 0 || endMS <= startMS {
		return errors.New("invalid WAV slice interval")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := ReadWAVInfo(source)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 16 || info.SampleRate <= 0 || info.Channels <= 0 {
		return errors.New("slicing supports PCM16 WAV only")
	}
	bytesPerFrame := int64(info.Channels * 2)
	startFrame := startMS * int64(info.SampleRate) / 1000
	endFrame := endMS * int64(info.SampleRate) / 1000
	startByte := startFrame * bytesPerFrame
	endByte := endFrame * bytesPerFrame
	if startByte >= info.DataSize {
		return errors.New("WAV slice starts after end of audio")
	}
	if endByte > info.DataSize {
		endByte = info.DataSize
	}
	endByte -= endByte % bytesPerFrame
	if endByte <= startByte {
		return errors.New("WAV slice is empty")
	}
	if _, err := source.Seek(info.DataOffset+startByte, io.SeekStart); err != nil {
		return err
	}
	writer, err := NewWAVWriter(outputPath, info.SampleRate, info.Channels)
	if err != nil {
		return err
	}
	remaining := endByte - startByte
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
		n, readErr := io.ReadFull(source, buf[:want])
		if n > 0 {
			if writeErr := writer.WritePCM16(buf[:n]); writeErr != nil {
				_ = writer.Close()
				return writeErr
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			_ = writer.Close()
			return readErr
		}
	}
	return writer.Close()
}

// CreateSilentPCM16WAVLike creates a valid silent companion with exactly the
// same PCM format and duration as source. Imported single-stream recordings use
// it so the regular two-input mixer can run without duplicating the source.
func CreateSilentPCM16WAVLike(sourcePath, outputPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	info, err := ReadWAVInfo(source)
	_ = source.Close()
	if err != nil {
		return err
	}
	if info.BitsPerSample != 16 || info.SampleRate <= 0 || (info.Channels != 1 && info.Channels != 2) {
		return errors.New("import supports PCM16 mono or stereo WAV only")
	}
	writer, err := NewWAVWriter(outputPath, info.SampleRate, info.Channels)
	if err != nil {
		return err
	}
	remaining := info.DataSize
	zeros := make([]byte, 64*1024)
	for remaining > 0 {
		n := int64(len(zeros))
		if remaining < n {
			n = remaining
		}
		if n%2 != 0 {
			n--
		}
		if n == 0 {
			break
		}
		if err := writer.WritePCM16(zeros[:n]); err != nil {
			_ = writer.Close()
			return err
		}
		remaining -= n
	}
	return writer.Close()
}

// ReadWAVInfo parses the RIFF header of an open WAV file.
func ReadWAVInfo(f *os.File) (WAVInfo, error) {
	var info WAVInfo
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return info, err
	}
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return info, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return info, errors.New("not a RIFF/WAVE file")
	}
	var formatFound, dataFound bool
	for {
		chunkHeader := make([]byte, 8)
		if _, err := io.ReadFull(f, chunkHeader); err != nil {
			return info, fmt.Errorf("WAV chunk header: %w", err)
		}
		chunkID := string(chunkHeader[:4])
		chunkSize := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		chunkStart, _ := f.Seek(0, io.SeekCurrent)
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return info, errors.New("invalid WAV fmt chunk")
			}
			format := make([]byte, 16)
			if _, err := io.ReadFull(f, format); err != nil {
				return info, err
			}
			if code := binary.LittleEndian.Uint16(format[0:2]); code != 1 {
				return info, fmt.Errorf("compressed WAV format %d is not supported", code)
			}
			info.Channels = int(binary.LittleEndian.Uint16(format[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(format[4:8]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(format[14:16]))
			formatFound = true
		case "data":
			info.DataOffset = chunkStart
			info.DataSize = chunkSize
			dataFound = true
		}
		if formatFound && dataFound {
			if _, err := f.Seek(info.DataOffset, io.SeekStart); err != nil {
				return info, err
			}
			return info, nil
		}
		next := chunkStart + chunkSize
		if chunkSize%2 != 0 {
			next++
		}
		if _, err := f.Seek(next, io.SeekStart); err != nil {
			return info, err
		}
	}
}

// MixPCM16WAV mixes two WAV files into one, applying per-source gain.
func MixPCM16WAV(micPath, systemPath, outputPath string, micGain, systemGain float64) error {
	mic, err := os.Open(micPath)
	if err != nil {
		return err
	}
	defer mic.Close()
	sys, err := os.Open(systemPath)
	if err != nil {
		return err
	}
	defer sys.Close()
	mi, err := ReadWAVInfo(mic)
	if err != nil {
		return err
	}
	si, err := ReadWAVInfo(sys)
	if err != nil {
		return err
	}
	if mi.SampleRate != si.SampleRate || mi.Channels != si.Channels || mi.BitsPerSample != 16 || si.BitsPerSample != 16 {
		return errors.New("WAV streams are incompatible")
	}
	out, err := NewWAVWriter(outputPath, mi.SampleRate, mi.Channels)
	if err != nil {
		return err
	}
	defer out.Close()
	micData := io.LimitReader(mic, mi.DataSize)
	systemData := io.LimitReader(sys, si.DataSize)
	mb, sb := make([]byte, 8192), make([]byte, 8192)
	for {
		mn, me := micData.Read(mb)
		sn, se := systemData.Read(sb)
		n := mn
		if sn > n {
			n = sn
		}
		if n == 0 {
			if me == io.EOF && se == io.EOF {
				break
			}
			if me != nil && me != io.EOF {
				return me
			}
			if se != nil && se != io.EOF {
				return se
			}
			continue
		}
		if n%2 != 0 {
			n--
		}
		mixed := make([]byte, n)
		for i := 0; i < n; i += 2 {
			var mv, sv int16
			if i+1 < mn {
				mv = int16(binary.LittleEndian.Uint16(mb[i : i+2]))
			}
			if i+1 < sn {
				sv = int16(binary.LittleEndian.Uint16(sb[i : i+2]))
			}
			v := int(float64(mv)*micGain + float64(sv)*systemGain)
			if v > 32767 {
				v = 32767
			}
			if v < -32768 {
				v = -32768
			}
			binary.LittleEndian.PutUint16(mixed[i:i+2], uint16(int16(v)))
		}
		if err := out.WritePCM16(mixed); err != nil {
			return err
		}
	}
	return out.Close()
}
