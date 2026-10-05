package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hajimehoshi/go-mp3"
	"github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
)

// NormalizeImportedAudio decodes a supported upload and atomically produces
// the canonical LocalMeetAssist format: PCM16 mono WAV at 16 kHz. The upload itself
// is only a temporary file and can be removed after this function succeeds.
func NormalizeImportedAudio(sourcePath, targetPath string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	var signature [12]byte
	n, readErr := io.ReadFull(f, signature[:])
	_ = f.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	part := targetPath + ".normalize.part"
	_ = os.Remove(part)
	defer os.Remove(part)
	switch {
	case n >= 12 && string(signature[0:4]) == "RIFF" && string(signature[8:12]) == "WAVE":
		err = normalizePCM16WAV(sourcePath, part)
	case n >= 4 && string(signature[0:4]) == "OggS":
		err = decodeOggOpus(sourcePath, part)
	case n >= 3 && (string(signature[0:3]) == "ID3" || signature[0] == 0xff):
		err = decodeMP3(sourcePath, part)
	default:
		err = errors.New("WAV, Ogg/Opus and MP3 are supported")
	}
	if err != nil {
		return err
	}
	return replaceFile(part, targetPath)
}

func normalizePCM16WAV(sourcePath, targetPath string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := ReadWAVInfo(f)
	if err != nil {
		return fmt.Errorf("unsupported WAV: %w", err)
	}
	if info.BitsPerSample != 16 || (info.Channels != 1 && info.Channels != 2) {
		return fmt.Errorf("PCM16 WAV mono/stereo required; got %d bit, %d channel(s)", info.BitsPerSample, info.Channels)
	}
	if _, err := f.Seek(info.DataOffset, io.SeekStart); err != nil {
		return err
	}
	return resamplePCM16(bufio.NewReader(io.LimitReader(f, info.DataSize)), targetPath, info.SampleRate, info.Channels)
}

func decodeMP3(sourcePath, targetPath string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder, err := mp3.NewDecoder(f)
	if err != nil {
		return fmt.Errorf("could not decode MP3: %w", err)
	}
	// go-mp3 exposes interleaved PCM16LE stereo.
	return resamplePCM16(bufio.NewReader(decoder), targetPath, decoder.SampleRate(), 2)
}

func decodeOggOpus(sourcePath, targetPath string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()
	reader, _, err := oggreader.NewWith(f)
	if err != nil {
		return fmt.Errorf("could not read Ogg/Opus: %w", err)
	}
	decoder, err := opus.NewDecoderWithOutput(16000, 1)
	if err != nil {
		return err
	}
	w, err := NewWAVWriter(targetPath, 16000, 1)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = w.Close()
			_ = os.Remove(targetPath)
		}
	}()
	pcm := make([]int16, 16000*120/1000)
	raw := make([]byte, len(pcm)*2)
	for {
		packet, _, packetErr := reader.ParseNextPacket()
		if packetErr == io.EOF {
			break
		}
		if packetErr != nil {
			return packetErr
		}
		if strings.HasPrefix(string(packet), "OpusTags") {
			continue
		}
		count, decodeErr := decoder.DecodeToInt16(packet, pcm)
		if decodeErr != nil {
			return fmt.Errorf("could not decode Opus: %w", decodeErr)
		}
		for i := 0; i < count; i++ {
			binary.LittleEndian.PutUint16(raw[i*2:i*2+2], uint16(pcm[i]))
		}
		if err := w.WritePCM16(raw[:count*2]); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func resamplePCM16(reader *bufio.Reader, targetPath string, sourceRate, channels int) error {
	if sourceRate <= 0 || (channels != 1 && channels != 2) {
		return errors.New("invalid PCM parameters")
	}
	w, err := NewWAVWriter(targetPath, 16000, 1)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = w.Close()
			_ = os.Remove(targetPath)
		}
	}()
	frame := make([]byte, channels*2)
	out := make([]byte, 0, 32*1024)
	var sourceIndex, outputIndex int64
	var downsampleSum int64
	var downsampleCount int64
	for {
		_, readErr := io.ReadFull(reader, frame)
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		sample := int32(int16(binary.LittleEndian.Uint16(frame[0:2])))
		if channels == 2 {
			sample = (sample + int32(int16(binary.LittleEndian.Uint16(frame[2:4])))) / 2
		}
		if sourceRate >= 16000 {
			downsampleSum += int64(sample)
			downsampleCount++
			if (sourceIndex+1)*16000 >= (outputIndex+1)*int64(sourceRate) {
				sample = int32(downsampleSum / downsampleCount)
				out = binary.LittleEndian.AppendUint16(out, uint16(int16(sample)))
				outputIndex++
				downsampleSum, downsampleCount = 0, 0
			}
		} else {
			for outputIndex*int64(sourceRate) <= sourceIndex*16000 {
				out = binary.LittleEndian.AppendUint16(out, uint16(int16(sample)))
				outputIndex++
			}
		}
		if len(out) >= 32*1024 {
			if err := w.WritePCM16(out); err != nil {
				return err
			}
			out = out[:0]
		}
		sourceIndex++
	}
	if len(out) > 0 {
		if err := w.WritePCM16(out); err != nil {
			return err
		}
	}
	if outputIndex == 0 {
		return errors.New("audio file is empty")
	}
	if err := w.Close(); err != nil {
		return err
	}
	success = true
	return nil
}
